package fakebluez

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus names used by BlueZ.
const (
	ServiceName      = "org.bluez"
	AdapterIface     = "org.bluez.Adapter1"
	DeviceIface      = "org.bluez.Device1"
	BatteryIface     = "org.bluez.Battery1"
	AgentIface       = "org.bluez.Agent1"
	AgentManagerPath = dbus.ObjectPath("/org/bluez")
	agentManager     = "org.bluez.AgentManager1"
	propertiesIface  = "org.freedesktop.DBus.Properties"
	objectManager    = "org.freedesktop.DBus.ObjectManager"
)

// Device describes a remote device to add to the fake.
type Device struct {
	Adapter   string // adapter name, defaults to "hci0"
	Address   string
	Name      string // empty means the Name property is absent
	Alias     string // defaults to Name, or the address when Name is empty
	Paired    bool
	Bonded    bool
	Trusted   bool
	Connected bool
	RSSI      int16 // 0 means the RSSI property is absent
	Battery   int   // <0 means no org.bluez.Battery1 interface
}

// Behavior scripts how a device reacts to Pair and Connect.
type Behavior struct {
	// ConnectDelay is how long Device1.Connect blocks before succeeding.
	ConnectDelay time.Duration
	// ConnectErr, when set, is returned by Device1.Connect after ConnectDelay.
	ConnectErr *dbus.Error
	// PairErr, when set, is returned by Device1.Pair.
	PairErr *dbus.Error
	// AgentRequest is the Agent1 method BlueZ calls on the registered agent
	// while pairing (RequestConfirmation, RequestAuthorization,
	// RequestPasskey, RequestPinCode, DisplayPasskey or DisplayPinCode).
	AgentRequest string
	Passkey      uint32
	PinCode      string
	// BondDelay delays Bonded/Paired after Connect for an unpaired device.
	BondDelay time.Duration
}

// AgentReply records the outcome of a call BlueZ made into the agent.
type AgentReply struct {
	Method string
	Device dbus.ObjectPath
	Value  any // the passkey or PIN returned by RequestPasskey/RequestPinCode
	Err    error
}

// AgentState describes the agent registered with the fake AgentManager1.
type AgentState struct {
	Sender     string
	Path       dbus.ObjectPath
	Capability string
	Default    bool
}

type objectMap = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// BlueZ is a fake bluetoothd. All exported methods are safe for concurrent use.
type BlueZ struct {
	tb   testing.TB
	conn *dbus.Conn
	addr string

	mu        sync.Mutex
	objects   objectMap
	behaviors map[dbus.ObjectPath]Behavior
	calls     map[string]int
	agent     *AgentState
	replies   []AgentReply
	onStart   func(b *BlueZ)
}

// New starts a private bus with a fake bluetoothd on it and points
// DBUS_SYSTEM_BUS_ADDRESS at it. Tests using it must not call t.Parallel.
func New(tb testing.TB) *BlueZ {
	tb.Helper()
	addr := StartBus(tb)
	conn, err := dbus.Connect(addr)
	if err != nil {
		tb.Fatalf("connect to test bus: %v", err)
	}
	tb.Cleanup(func() { _ = conn.Close() })

	reply, err := conn.RequestName(ServiceName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		tb.Fatalf("claim %s on test bus: reply=%v err=%v", ServiceName, reply, err)
	}

	b := &BlueZ{
		tb:        tb,
		conn:      conn,
		addr:      addr,
		objects:   objectMap{},
		behaviors: map[dbus.ObjectPath]Behavior{},
		calls:     map[string]int{},
	}
	b.export(&objectManagerHandler{b}, "/", objectManager)
	b.export(&agentManagerHandler{b}, AgentManagerPath, agentManager)
	tb.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	return b
}

// Address returns the private bus address.
func (b *BlueZ) Address() string { return b.addr }

func (b *BlueZ) export(v any, path dbus.ObjectPath, iface string) {
	if err := b.conn.Export(v, path, iface); err != nil {
		b.tb.Fatalf("export %s on %s: %v", iface, path, err)
	}
}

// AdapterPath returns the object path of the named adapter.
func AdapterPath(name string) dbus.ObjectPath { return dbus.ObjectPath("/org/bluez/" + name) }

// DevicePath returns the object path of a device under the named adapter.
func DevicePath(adapter, address string) dbus.ObjectPath {
	return dbus.ObjectPath(fmt.Sprintf("/org/bluez/%s/dev_%s", adapter, strings.ReplaceAll(strings.ToUpper(address), ":", "_")))
}

// AddAdapter adds an hci adapter and returns its object path.
func (b *BlueZ) AddAdapter(name, address string, powered bool) dbus.ObjectPath {
	path := AdapterPath(name)
	props := map[string]dbus.Variant{
		"Address":             dbus.MakeVariant(address),
		"Name":                dbus.MakeVariant("fake-" + name),
		"Alias":               dbus.MakeVariant("fake-" + name),
		"Powered":             dbus.MakeVariant(powered),
		"Discoverable":        dbus.MakeVariant(false),
		"DiscoverableTimeout": dbus.MakeVariant(uint32(180)),
		"Discovering":         dbus.MakeVariant(false),
	}
	b.mu.Lock()
	b.objects[path] = map[string]map[string]dbus.Variant{AdapterIface: props}
	b.mu.Unlock()
	b.export(&propertiesHandler{b, path}, path, propertiesIface)
	b.export(&adapterHandler{b, path}, path, AdapterIface)
	b.emitAdded(path)
	return path
}

// AddDevice adds a remote device (announcing it with InterfacesAdded) and
// returns its object path.
func (b *BlueZ) AddDevice(d Device) dbus.ObjectPath {
	if d.Adapter == "" {
		d.Adapter = "hci0"
	}
	path := DevicePath(d.Adapter, d.Address)
	alias := d.Alias
	if alias == "" {
		alias = d.Name
	}
	if alias == "" {
		alias = strings.ReplaceAll(strings.ToUpper(d.Address), ":", "-")
	}
	props := map[string]dbus.Variant{
		"Address":          dbus.MakeVariant(strings.ToUpper(d.Address)),
		"Alias":            dbus.MakeVariant(alias),
		"Adapter":          dbus.MakeVariant(AdapterPath(d.Adapter)),
		"Paired":           dbus.MakeVariant(d.Paired),
		"Bonded":           dbus.MakeVariant(d.Bonded),
		"Trusted":          dbus.MakeVariant(d.Trusted),
		"Connected":        dbus.MakeVariant(d.Connected),
		"ServicesResolved": dbus.MakeVariant(d.Connected),
	}
	if d.Name != "" {
		props["Name"] = dbus.MakeVariant(d.Name)
	}
	if d.RSSI != 0 {
		props["RSSI"] = dbus.MakeVariant(d.RSSI)
	}
	ifaces := map[string]map[string]dbus.Variant{DeviceIface: props}
	if d.Battery >= 0 {
		ifaces[BatteryIface] = map[string]dbus.Variant{"Percentage": dbus.MakeVariant(byte(d.Battery))}
	}
	b.mu.Lock()
	b.objects[path] = ifaces
	b.mu.Unlock()
	b.export(&propertiesHandler{b, path}, path, propertiesIface)
	b.export(&deviceHandler{b, path}, path, DeviceIface)
	b.emitAdded(path)
	return path
}

// OnStartDiscovery sets a function that runs in its own goroutine after
// Adapter1.StartDiscovery returns. Tests use it to "discover" devices.
func (b *BlueZ) OnStartDiscovery(fn func(b *BlueZ)) {
	b.mu.Lock()
	b.onStart = fn
	b.mu.Unlock()
}

// SetBehavior scripts how the device at path reacts to Pair and Connect.
func (b *BlueZ) SetBehavior(path dbus.ObjectPath, bh Behavior) {
	b.mu.Lock()
	b.behaviors[path] = bh
	b.mu.Unlock()
}

// SetProperty changes a property and emits PropertiesChanged.
func (b *BlueZ) SetProperty(path dbus.ObjectPath, iface, name string, value any) {
	b.mu.Lock()
	ifaces, ok := b.objects[path]
	if !ok || ifaces[iface] == nil {
		b.mu.Unlock()
		b.tb.Errorf("fakebluez: SetProperty on missing %s %s", path, iface)
		return
	}
	v := dbus.MakeVariant(value)
	ifaces[iface][name] = v
	b.mu.Unlock()
	_ = b.conn.Emit(path, propertiesIface+".PropertiesChanged", iface, map[string]dbus.Variant{name: v}, []string{})
}

// Property returns the current value of a property, or nil when absent.
func (b *BlueZ) Property(path dbus.ObjectPath, iface, name string) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.objects[path][iface][name]; ok {
		return v.Value()
	}
	return nil
}

// HasObject reports whether an object exists at path.
func (b *BlueZ) HasObject(path dbus.ObjectPath) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[path]
	return ok
}

// Calls returns how often a method (e.g. "GetManagedObjects",
// "Device1.Connect", "Properties.Get") has been called.
func (b *BlueZ) Calls(method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[method]
}

// ResetCalls clears the call counters.
func (b *BlueZ) ResetCalls() {
	b.mu.Lock()
	b.calls = map[string]int{}
	b.mu.Unlock()
}

func (b *BlueZ) count(method string) {
	b.mu.Lock()
	b.calls[method]++
	b.mu.Unlock()
}

// Agent returns the currently registered agent, or nil.
func (b *BlueZ) Agent() *AgentState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.agent == nil {
		return nil
	}
	a := *b.agent
	return &a
}

// AgentReplies returns every agent call outcome recorded so far.
func (b *BlueZ) AgentReplies() []AgentReply {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]AgentReply(nil), b.replies...)
}

// CallAgent invokes an org.bluez.Agent1 method on the registered agent the
// way bluetoothd would, records the outcome and returns the reply error.
func (b *BlueZ) CallAgent(ctx context.Context, method string, device dbus.ObjectPath, args ...any) (any, error) {
	agent := b.Agent()
	if agent == nil {
		return nil, fmt.Errorf("fakebluez: no agent registered")
	}
	callArgs := append([]any{device}, args...)
	call := b.conn.Object(agent.Sender, agent.Path).CallWithContext(ctx, AgentIface+"."+method, 0, callArgs...)
	var value any
	if call.Err == nil && len(call.Body) > 0 {
		value = call.Body[0]
	}
	b.mu.Lock()
	b.replies = append(b.replies, AgentReply{Method: method, Device: device, Value: value, Err: call.Err})
	b.mu.Unlock()
	return value, call.Err
}

// CancelAgent calls Agent1.Cancel on the registered agent.
func (b *BlueZ) CancelAgent(ctx context.Context) error {
	agent := b.Agent()
	if agent == nil {
		return fmt.Errorf("fakebluez: no agent registered")
	}
	return b.conn.Object(agent.Sender, agent.Path).CallWithContext(ctx, AgentIface+".Cancel", 0).Err
}

func (b *BlueZ) emitAdded(path dbus.ObjectPath) {
	b.mu.Lock()
	ifaces := copyIfaces(b.objects[path])
	b.mu.Unlock()
	_ = b.conn.Emit("/", objectManager+".InterfacesAdded", path, ifaces)
}

func (b *BlueZ) removeObject(path dbus.ObjectPath) {
	b.mu.Lock()
	ifaces := b.objects[path]
	delete(b.objects, path)
	b.mu.Unlock()
	names := make([]string, 0, len(ifaces))
	for name := range ifaces {
		names = append(names, name)
	}
	_ = b.conn.Emit("/", objectManager+".InterfacesRemoved", path, names)
}

func (b *BlueZ) boolProp(path dbus.ObjectPath, iface, name string) bool {
	v, _ := b.Property(path, iface, name).(bool)
	return v
}

func copyIfaces(in map[string]map[string]dbus.Variant) map[string]map[string]dbus.Variant {
	out := make(map[string]map[string]dbus.Variant, len(in))
	for iface, props := range in {
		p := make(map[string]dbus.Variant, len(props))
		for k, v := range props {
			p[k] = v
		}
		out[iface] = p
	}
	return out
}

func bluezError(name, msg string) *dbus.Error {
	return dbus.NewError("org.bluez.Error."+name, []any{msg})
}

// --- org.freedesktop.DBus.ObjectManager on "/" ---

type objectManagerHandler struct{ b *BlueZ }

func (h *objectManagerHandler) GetManagedObjects() (objectMap, *dbus.Error) {
	h.b.count("GetManagedObjects")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	out := make(objectMap, len(h.b.objects))
	for path, ifaces := range h.b.objects {
		out[path] = copyIfaces(ifaces)
	}
	return out, nil
}

// --- org.bluez.AgentManager1 on /org/bluez ---

type agentManagerHandler struct{ b *BlueZ }

func (h *agentManagerHandler) RegisterAgent(sender dbus.Sender, path dbus.ObjectPath, capability string) *dbus.Error {
	h.b.count("AgentManager1.RegisterAgent")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	if h.b.agent != nil && h.b.agent.Sender == string(sender) {
		return bluezError("AlreadyExists", "Already Exists")
	}
	h.b.agent = &AgentState{Sender: string(sender), Path: path, Capability: capability}
	return nil
}

func (h *agentManagerHandler) RequestDefaultAgent(sender dbus.Sender, path dbus.ObjectPath) *dbus.Error {
	h.b.count("AgentManager1.RequestDefaultAgent")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	// Like bluetoothd, look the agent up by the caller's bus name.
	if h.b.agent == nil || h.b.agent.Sender != string(sender) || h.b.agent.Path != path {
		return bluezError("DoesNotExist", "Does Not Exist")
	}
	h.b.agent.Default = true
	return nil
}

func (h *agentManagerHandler) UnregisterAgent(sender dbus.Sender, path dbus.ObjectPath) *dbus.Error {
	h.b.count("AgentManager1.UnregisterAgent")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	if h.b.agent == nil || h.b.agent.Sender != string(sender) || h.b.agent.Path != path {
		return bluezError("DoesNotExist", "Does Not Exist")
	}
	h.b.agent = nil
	return nil
}

// --- org.freedesktop.DBus.Properties on every adapter and device ---

type propertiesHandler struct {
	b    *BlueZ
	path dbus.ObjectPath
}

func (h *propertiesHandler) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	h.b.count("Properties.Get")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	props, ok := h.b.objects[h.path][iface]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"No such interface '" + iface + "'"})
	}
	v, ok := props[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"No such property '" + name + "'"})
	}
	return v, nil
}

func (h *propertiesHandler) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	h.b.count("Properties.GetAll")
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	out := map[string]dbus.Variant{}
	for k, v := range h.b.objects[h.path][iface] {
		out[k] = v
	}
	return out, nil
}

func (h *propertiesHandler) Set(iface, name string, value dbus.Variant) *dbus.Error {
	h.b.count("Properties.Set")
	h.b.count("Properties.Set:" + name)
	h.b.mu.Lock()
	props, ok := h.b.objects[h.path][iface]
	if !ok {
		h.b.mu.Unlock()
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"No such interface '" + iface + "'"})
	}
	if _, ok := props[name]; !ok {
		h.b.mu.Unlock()
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"No such property '" + name + "'"})
	}
	h.b.mu.Unlock()
	h.b.SetProperty(h.path, iface, name, value.Value())
	return nil
}

// --- org.bluez.Adapter1 ---

type adapterHandler struct {
	b    *BlueZ
	path dbus.ObjectPath
}

func (h *adapterHandler) StartDiscovery() *dbus.Error {
	h.b.count("Adapter1.StartDiscovery")
	if !h.b.boolProp(h.path, AdapterIface, "Powered") {
		return bluezError("NotReady", "Resource Not Ready")
	}
	h.b.SetProperty(h.path, AdapterIface, "Discovering", true)
	h.b.mu.Lock()
	fn := h.b.onStart
	h.b.mu.Unlock()
	if fn != nil {
		go fn(h.b)
	}
	return nil
}

func (h *adapterHandler) StopDiscovery() *dbus.Error {
	h.b.count("Adapter1.StopDiscovery")
	h.b.SetProperty(h.path, AdapterIface, "Discovering", false)
	return nil
}

func (h *adapterHandler) RemoveDevice(device dbus.ObjectPath) *dbus.Error {
	h.b.count("Adapter1.RemoveDevice")
	if !h.b.HasObject(device) || !strings.HasPrefix(string(device), string(h.path)+"/") {
		return bluezError("DoesNotExist", "Does Not Exist")
	}
	h.b.removeObject(device)
	return nil
}

// --- org.bluez.Device1 ---

type deviceHandler struct {
	b    *BlueZ
	path dbus.ObjectPath
}

func (h *deviceHandler) behavior() Behavior {
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	return h.b.behaviors[h.path]
}

// runAgentRequest performs the scripted agent interaction for a pairing.
func (h *deviceHandler) runAgentRequest(bh Behavior) *dbus.Error {
	if bh.AgentRequest == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var args []any
	switch bh.AgentRequest {
	case "RequestConfirmation":
		args = []any{bh.Passkey}
	case "DisplayPasskey":
		args = []any{bh.Passkey, uint16(0)}
	case "DisplayPinCode":
		args = []any{bh.PinCode}
	}
	value, err := h.b.CallAgent(ctx, bh.AgentRequest, h.path, args...)
	if err != nil {
		return bluezError("AuthenticationFailed", "Authentication Failed")
	}
	switch bh.AgentRequest {
	case "RequestPasskey":
		if v, ok := value.(uint32); !ok || v != bh.Passkey {
			return bluezError("AuthenticationFailed", "Authentication Failed")
		}
	case "RequestPinCode":
		if v, ok := value.(string); !ok || (bh.PinCode != "" && v != bh.PinCode) {
			return bluezError("AuthenticationFailed", "Authentication Failed")
		}
	}
	return nil
}

func (h *deviceHandler) Pair() *dbus.Error {
	h.b.count("Device1.Pair")
	bh := h.behavior()
	if bh.PairErr != nil {
		return bh.PairErr
	}
	if h.b.boolProp(h.path, DeviceIface, "Paired") {
		return bluezError("AlreadyExists", "Already Exists")
	}
	if err := h.runAgentRequest(bh); err != nil {
		return err
	}
	// Like bluetoothd: pairing opens a link, over which services are resolved.
	h.b.SetProperty(h.path, DeviceIface, "Bonded", true)
	h.b.SetProperty(h.path, DeviceIface, "Paired", true)
	h.b.SetProperty(h.path, DeviceIface, "ServicesResolved", true)
	return nil
}

func (h *deviceHandler) CancelPairing() *dbus.Error {
	h.b.count("Device1.CancelPairing")
	return nil
}

func (h *deviceHandler) Connect() *dbus.Error {
	h.b.count("Device1.Connect")
	bh := h.behavior()
	if bh.ConnectDelay > 0 {
		time.Sleep(bh.ConnectDelay)
	}
	if bh.ConnectErr != nil {
		return bh.ConnectErr
	}
	if h.b.boolProp(h.path, DeviceIface, "Connected") {
		return bluezError("AlreadyConnected", "Already Connected")
	}
	h.b.SetProperty(h.path, DeviceIface, "Connected", true)
	if !h.b.boolProp(h.path, DeviceIface, "Paired") {
		if err := h.runAgentRequest(bh); err != nil {
			h.b.SetProperty(h.path, DeviceIface, "Connected", false)
			return err
		}
		go func() {
			time.Sleep(bh.BondDelay)
			h.b.SetProperty(h.path, DeviceIface, "Bonded", true)
			h.b.SetProperty(h.path, DeviceIface, "Paired", true)
		}()
	}
	h.b.SetProperty(h.path, DeviceIface, "ServicesResolved", true)
	return nil
}

func (h *deviceHandler) Disconnect() *dbus.Error {
	h.b.count("Device1.Disconnect")
	if !h.b.boolProp(h.path, DeviceIface, "Connected") {
		return bluezError("NotConnected", "Not Connected")
	}
	h.b.SetProperty(h.path, DeviceIface, "ServicesResolved", false)
	h.b.SetProperty(h.path, DeviceIface, "Connected", false)
	return nil
}
