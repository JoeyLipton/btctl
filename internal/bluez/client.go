// Package bluez wraps the parts of the BlueZ D-Bus API btctl uses in a small,
// typed client, plus the pairing agent (agent.go). It has no user interface:
// callers decide how to report results. Every method that talks to the bus
// takes a context.Context and applies a per-call timeout, so a stuck
// bluetoothd cannot wedge the CLI.
package bluez

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// managedObjects is the shape returned by ObjectManager.GetManagedObjects:
// path -> interface -> property -> variant.
type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Client wraps a system D-Bus connection used to talk to BlueZ.
type Client struct {
	conn     *dbus.Conn
	verbose  bool
	log      io.Writer // warnings and, with verbose, debug lines
	timeouts timeouts

	mu              sync.Mutex // guards the fields below
	adapterName     string     // preferred adapter ("hci1"); "" means the first one
	strictAdapter   bool       // fail instead of falling back when adapterName is missing
	activeAdapter   dbus.ObjectPath
	connectionLocks map[dbus.ObjectPath]*sync.Mutex
	agent           *Agent // registered pairing agent, if any
}

// Options configures New.
type Options struct {
	// Verbose logs every outgoing D-Bus method call to stderr.
	Verbose bool
	// Adapter selects the adapter by name (e.g. "hci1"). Empty selects the
	// first adapter BlueZ reports.
	Adapter string
	// AdapterOptional falls back to the first adapter when Adapter does not
	// exist instead of failing. It is used for the remembered adapter, which
	// may be a USB dongle that has since been unplugged.
	AdapterOptional bool
	// Log receives warnings and, with Verbose, debug lines. nil means stderr.
	Log io.Writer
}

// timeouts groups every deadline the client applies. They live on the Client
// (initialised from the package constants) so tests can shorten them.
type timeouts struct {
	call             time.Duration // ordinary D-Bus method calls
	connect          time.Duration // Device1.Connect, which blocks until the link is up
	pair             time.Duration // Device1.Pair, which blocks while the agent is consulted
	connection       time.Duration // the whole connect-and-bond wait
	bonding          time.Duration // Bonded/Paired after Connected for new devices
	servicesResolved time.Duration
	disconnect       time.Duration
}

func defaultTimeouts() timeouts {
	return timeouts{
		call:             dbusCallTimeout,
		connect:          connectCallTimeout,
		pair:             pairCallTimeout,
		connection:       pairingTimeout,
		bonding:          bondingTimeout,
		servicesResolved: servicesResolvedTimeout,
		disconnect:       disconnectTimeout,
	}
}

// Device is a snapshot of a remote Bluetooth device exposed by BlueZ.
type Device struct {
	Path      dbus.ObjectPath // BlueZ object path, e.g. /org/bluez/hci0/dev_AA_BB...
	Name      string          // alias or advertised name; "" when the device has none
	Address   string          // MAC address in AA:BB:CC:DD:EE:FF format
	Connected bool
	Paired    bool
	Trusted   bool
	RSSI      int16 // signal strength in dBm; 0 if not available
	Battery   int   // charge percentage 0-100; BatteryUnavailable (-1) if absent
}

// DisplayName returns the device's name, or "(unknown)" when it has none.
func (d Device) DisplayName() string {
	if d.Name == "" {
		return "(unknown)"
	}
	return d.Name
}

// Status returns "connected", "paired" or "" for display.
func (d Device) Status() string {
	switch {
	case d.Connected:
		return "connected"
	case d.Paired:
		return "paired"
	default:
		return ""
	}
}

var (
	addressPattern     = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	adapterNamePattern = regexp.MustCompile(`^hci[0-9]+$`)
)

// IsAddress reports whether s is a colon-separated 48-bit MAC address.
func IsAddress(s string) bool { return addressPattern.MatchString(s) }

// IsAdapterName reports whether s is a BlueZ adapter name such as hci0.
func IsAdapterName(s string) bool { return adapterNamePattern.MatchString(s) }

// New opens a private connection to the system bus. It makes no BlueZ calls:
// every operation fetches what it needs, and the active adapter (see Options)
// is resolved from each object snapshot, so the first operation reports
// "BlueZ is not running" or a missing -a adapter.
//
// The connection is private (not godbus's shared SystemBus) because Close
// tears it down; closing the shared connection would break any other user of
// it in the same process. DBUS_SYSTEM_BUS_ADDRESS is honoured, which is how the
// tests point the client at a fake bluetoothd.
func New(opts Options) (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system D-Bus: %w", err)
	}
	log := opts.Log
	if log == nil {
		log = os.Stderr
	}
	return &Client{
		conn:          conn,
		verbose:       opts.Verbose,
		log:           log,
		timeouts:      defaultTimeouts(),
		adapterName:   opts.Adapter,
		strictAdapter: opts.Adapter != "" && !opts.AdapterOptional,
	}, nil
}

// AdapterState is the active adapter and its paired devices.
type AdapterState struct {
	Adapter Adapter  // the active adapter; zero when Found is false
	Found   bool     // whether BlueZ has an adapter at all
	Devices []Device // paired devices, connected first (see ListDevices)
}

// State returns the active adapter and its paired devices from a single
// GetManagedObjects call. Status bars poll `btctl status` every few seconds,
// and every call returns all objects BlueZ knows (often hundreds of cached
// BLE devices), so status reads everything at once.
func (c *Client) State(ctx context.Context) (AdapterState, error) {
	objects, adapterPath, err := c.snapshot(ctx)
	if err != nil {
		return AdapterState{}, fmt.Errorf("read state: %w", err)
	}
	if adapterPath == "" {
		return AdapterState{}, nil
	}
	return AdapterState{
		Adapter: parseAdapter(adapterPath, objects[adapterPath][bluezAdapterIface]),
		Found:   true,
		Devices: pairedDevices(objects, adapterPath),
	}, nil
}

// snapshot fetches the BlueZ object tree and resolves the active adapter in
// it. The adapter path is "" when BlueZ has no adapters.
func (c *Client) snapshot(ctx context.Context) (managedObjects, dbus.ObjectPath, error) {
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return nil, "", err
	}
	path, err := c.resolveAdapter(objects)
	if err != nil {
		return objects, "", err
	}
	return objects, path, nil
}

// resolveAdapter picks the active adapter from objects: the preferred one when
// it exists, otherwise the first adapter (unless the preference is strict).
func (c *Client) resolveAdapter(objects managedObjects) (dbus.ObjectPath, error) {
	adapters := adaptersIn(objects)
	c.mu.Lock()
	defer c.mu.Unlock()
	var chosen dbus.ObjectPath
	if c.adapterName != "" {
		for _, a := range adapters {
			if a.Name == c.adapterName {
				chosen = a.Path
				break
			}
		}
		if chosen == "" && c.strictAdapter {
			c.activeAdapter = ""
			return "", fmt.Errorf("%w: %s", ErrAdapterNotFound, c.adapterName)
		}
	}
	if chosen == "" && len(adapters) > 0 {
		chosen = adapters[0].Path
	}
	c.activeAdapter = chosen
	return chosen, nil
}

// Verbose reports whether the client logs D-Bus calls.
func (c *Client) Verbose() bool {
	if c == nil {
		return false
	}
	return c.verbose
}

// debugf writes a "[v]" line when the client is verbose.
func (c *Client) debugf(format string, args ...any) {
	if c.verbose {
		fmt.Fprintf(c.log, "[v] "+format+"\n", args...)
	}
}

// warnf writes a warning that does not stop the operation.
func (c *Client) warnf(format string, args ...any) {
	fmt.Fprintf(c.log, "[btctl] warning: "+format+"\n", args...)
}

// Close releases the underlying D-Bus connection. It is safe to call more
// than once. c.conn is deliberately not reset: goroutines that are still
// winding down may read it, and calls on a closed connection fail cleanly.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close D-Bus connection: %w", err)
	}
	return nil
}

// connLock returns the per-device mutex for path, creating it if needed.
// Hold c.mu only while retrieving the mutex; lock the returned mutex separately.
func (c *Client) connLock(path dbus.ObjectPath) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connectionLocks == nil {
		c.connectionLocks = make(map[dbus.ObjectPath]*sync.Mutex)
	}
	if _, ok := c.connectionLocks[path]; !ok {
		c.connectionLocks[path] = &sync.Mutex{}
	}
	return c.connectionLocks[path]
}

// DeviceName returns the name of the device at path (see Device.Name), or
// its address when it has none or cannot be read.
func (c *Client) DeviceName(ctx context.Context, path dbus.ObjectPath) string {
	address := deviceLabelFromPath(path)
	var props map[string]dbus.Variant
	if err := c.callCtxStore(ctx, path, dbusPropertiesIface, "GetAll", &props, bluezDeviceIface); err == nil {
		if name := deviceName(props, address); name != "" {
			return name
		}
	}
	return address
}

// deviceLabelFromPath turns a device object path such as
// /org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF into the MAC AA:BB:CC:DD:EE:FF.
func deviceLabelFromPath(path dbus.ObjectPath) string {
	s := string(path)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "dev_")
	return strings.ReplaceAll(s, "_", ":")
}

// ListDevices returns paired devices on the active adapter, sorted with
// connected devices first and ties broken alphabetically by name.
// Scanned-but-unpaired and cached strangers are excluded; Connected and
// Trusted are not filter conditions.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	objects, adapterPath, err := c.snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return pairedDevices(objects, adapterPath), nil
}

// pairedDevices returns the paired devices under adapterPath: connected
// first, then named devices alphabetically, then unnamed ones by address.
func pairedDevices(objects managedObjects, adapterPath dbus.ObjectPath) []Device {
	devices := make([]Device, 0)
	for _, d := range devicesIn(objects, adapterPath) {
		if d.Paired {
			devices = append(devices, d)
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		if (a.Name == "") != (b.Name == "") {
			return a.Name != ""
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.Address < b.Address
	})
	return devices
}

// FindDevice returns the device with the given address (case-insensitive)
// on the active adapter, whether it is paired or only known to BlueZ, for
// example from a recent scan.
func (c *Client) FindDevice(ctx context.Context, address string) (Device, error) {
	objects, adapterPath, err := c.snapshot(ctx)
	if err != nil {
		return Device{}, fmt.Errorf("find device %s: %w", address, err)
	}
	for _, d := range devicesIn(objects, adapterPath) {
		if strings.EqualFold(d.Address, address) {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("%w: %s", ErrDeviceNotFound, address)
}

// devicesIn returns every device under adapterPath in an object snapshot.
func devicesIn(objects managedObjects, adapterPath dbus.ObjectPath) []Device {
	if adapterPath == "" {
		return nil
	}
	// The trailing slash keeps hci1 from matching /org/bluez/hci10/....
	prefix := string(adapterPath) + "/"
	var devices []Device
	for path, ifaces := range objects {
		props, ok := ifaces[bluezDeviceIface]
		if !ok || !strings.HasPrefix(string(path), prefix) {
			continue
		}
		devices = append(devices, parseDevice(path, props, ifaces))
	}
	return devices
}

// callCtx invokes a method on the BlueZ service at path and waits
// synchronously for the reply.
func (c *Client) callCtx(ctx context.Context, path dbus.ObjectPath, iface, method string, args ...interface{}) error {
	return c.callCtxStore(ctx, path, iface, method, nil, args...)
}

// callCtxStore is the result-aware variant of callCtx.
func (c *Client) callCtxStore(ctx context.Context, path dbus.ObjectPath, iface, method string, dest interface{}, args ...interface{}) error {
	if c == nil {
		return fmt.Errorf("dbus call: bluetooth client is not connected")
	}
	return c.callTimeout(ctx, c.timeouts.call, path, iface, method, dest, args...)
}

// callTimeout is callCtxStore with an explicit timeout, for methods such as
// Device1.Connect and Device1.Pair that legitimately block for a long time.
func (c *Client) callTimeout(ctx context.Context, timeout time.Duration, path dbus.ObjectPath, iface, method string, dest interface{}, args ...interface{}) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("dbus call: bluetooth client is not connected")
	}
	c.debugf("call: %s.%s on %s", iface, method, path)

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	obj := c.conn.Object(bluezService, path)
	call := obj.CallWithContext(callCtx, iface+"."+method, 0, args...)
	if call.Err != nil {
		return fmt.Errorf("dbus call %s.%s on %s: %w", iface, method, path, call.Err)
	}
	if dest != nil {
		if err := call.Store(dest); err != nil {
			return fmt.Errorf("decode %s.%s response: %w", iface, method, err)
		}
	}
	return nil
}

// getManagedObjects invokes ObjectManager.GetManagedObjects on org.bluez "/".
func (c *Client) getManagedObjects(ctx context.Context) (managedObjects, error) {
	var objects managedObjects
	if err := c.callCtxStore(ctx, bluezRootPath, dbusObjectManagerIface, "GetManagedObjects", &objects); err != nil {
		return nil, err
	}
	return objects, nil
}

// findAdapterPath returns the active adapter path together with a
// GetManagedObjects snapshot.
func (c *Client) findAdapterPath(ctx context.Context) (dbus.ObjectPath, managedObjects, error) {
	objects, path, err := c.snapshot(ctx)
	if err != nil {
		return "", nil, err
	}
	if path == "" {
		return "", objects, fmt.Errorf("find adapter: %w", ErrNoAdapter)
	}
	return path, objects, nil
}

// getObjectProperty extracts a typed property from a D-Bus property map.
// out must be a pointer to string, bool, int16, or int. Returns false when
// the key is absent or the type does not match.
func getObjectProperty(props map[string]dbus.Variant, key string, out interface{}) bool {
	v, ok := props[key]
	if !ok {
		return false
	}
	switch p := out.(type) {
	case *string:
		s, ok := v.Value().(string)
		if !ok {
			return false
		}
		*p = s
		return true
	case *bool:
		b, ok := v.Value().(bool)
		if !ok {
			return false
		}
		*p = b
		return true
	case *int16:
		i, ok := v.Value().(int16)
		if !ok {
			return false
		}
		*p = i
		return true
	case *int:
		switch val := v.Value().(type) {
		case byte:
			*p = int(val)
			return true
		case int:
			*p = val
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// parseDevice extracts a Device from a property map.
func parseDevice(path dbus.ObjectPath, props map[string]dbus.Variant, ifaces map[string]map[string]dbus.Variant) Device {
	addr := ""
	getObjectProperty(props, "Address", &addr)
	name := deviceName(props, addr)
	var connected, paired, trusted bool
	getObjectProperty(props, propConnected, &connected)
	getObjectProperty(props, propPaired, &paired)
	getObjectProperty(props, propTrusted, &trusted)
	var rssi int16
	getObjectProperty(props, "RSSI", &rssi)
	return Device{
		Path:      path,
		Name:      name,
		Address:   addr,
		Connected: connected,
		Paired:    paired,
		Trusted:   trusted,
		Battery:   batteryFromInterfaces(ifaces),
		RSSI:      rssi,
	}
}

// deviceName returns the name to show for a device from a set of Device1
// properties, or "" when it has none. Alias wins over Name because it
// reflects names the user set (bluetoothctl set-alias, GNOME Settings); BlueZ
// falls back to Name for it. For a device with no name at all BlueZ sets
// Alias to the address with dashes, which is not a name.
func deviceName(props map[string]dbus.Variant, address string) string {
	var alias string
	if getObjectProperty(props, "Alias", &alias) && alias != "" && !aliasIsAddress(alias, address) {
		return alias
	}
	var name string
	if getObjectProperty(props, "Name", &name) {
		return name
	}
	return ""
}

// aliasIsAddress reports whether alias is BlueZ's placeholder for a device
// without a name: its address with dashes (or colons) as separators.
func aliasIsAddress(alias, address string) bool {
	normalized := strings.ReplaceAll(alias, "-", ":")
	if address != "" {
		return strings.EqualFold(normalized, address)
	}
	return len(normalized) == 17 && strings.Count(normalized, ":") == 5
}

// batteryFromInterfaces reads Percentage from org.bluez.Battery1 when present.
func batteryFromInterfaces(ifaces map[string]map[string]dbus.Variant) int {
	if ifaces == nil {
		return BatteryUnavailable
	}
	battProps, ok := ifaces[bluezBatteryIface]
	if !ok {
		return BatteryUnavailable
	}
	var pct int
	if getObjectProperty(battProps, propPercentage, &pct) {
		return pct
	}
	return BatteryUnavailable
}
