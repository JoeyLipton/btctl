// Package bluetooth wraps a subset of the BlueZ D-Bus API in a small, typed
// client. Every method that talks to the bus accepts a context.Context and
// applies an internal per-call timeout via callCtx so a stuck bluetoothd
// cannot wedge the CLI.
package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	timeouts timeouts

	mu              sync.Mutex // guards the fields below
	adapterName     string     // preferred adapter ("hci1"); "" means the first one
	strictAdapter   bool       // fail instead of falling back when adapterName is missing
	activeAdapter   dbus.ObjectPath
	connectionLocks map[dbus.ObjectPath]*sync.Mutex
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
	Name      string          // human-readable name; "Unknown" if not advertised
	Address   string          // MAC address in AA:BB:CC:DD:EE:FF format
	Connected bool
	Paired    bool
	Trusted   bool
	RSSI      int16 // signal strength in dBm; 0 if not available
	Battery   int   // charge percentage 0-100; batteryUnavailable (-1) if absent
}

// New opens a private connection to the system bus, verifies that org.bluez
// is reachable and resolves the active adapter (see Options).
//
// The connection is private (not godbus's shared SystemBus) because Close
// tears it down; closing the shared connection would break any other user of
// it in the same process. DBUS_SYSTEM_BUS_ADDRESS is honoured, which is how the
// tests point the client at a fake bluetoothd.
func New(ctx context.Context, opts Options) (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system D-Bus: %w", err)
	}
	c := &Client{
		conn:          conn,
		verbose:       opts.Verbose,
		timeouts:      defaultTimeouts(),
		adapterName:   opts.Adapter,
		strictAdapter: opts.Adapter != "" && !opts.AdapterOptional,
	}
	if _, _, err := c.snapshot(ctx); err != nil {
		_ = conn.Close()
		if errors.Is(err, ErrAdapterNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("probe BlueZ: %w", err)
	}
	return c, nil
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

// Verbose reports whether the client logs D-Bus calls to stderr.
func (c *Client) Verbose() bool {
	if c == nil {
		return false
	}
	return c.verbose
}

// Conn returns the underlying D-Bus connection. It exists so callers (e.g.
// main.go) can construct an Agent bound to the same bus connection the
// Client is using.
func (c *Client) Conn() *dbus.Conn {
	if c == nil {
		return nil
	}
	return c.conn
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

// getDeviceName returns the device's Name property, or a MAC-style label
// derived from the object path when the name is absent or empty.
func (c *Client) getDeviceName(ctx context.Context, path dbus.ObjectPath) string {
	if v, err := c.getProperty(ctx, path, bluezDeviceIface, "Name"); err == nil {
		if s, ok := v.Value().(string); ok && s != "" {
			return s
		}
	}
	return deviceLabelFromPath(path)
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
//
// A device is included only when Paired is true and its object path is rooted
// under the active adapter path. Scanned-but-unpaired and cached strangers are
// excluded. Connected and Trusted are not filter conditions.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	objects, adapterPath, err := c.snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	if adapterPath == "" {
		return []Device{}, nil
	}
	adapterPrefix := string(adapterPath)

	devices := make([]Device, 0)
	for path, ifaces := range objects {
		props, ok := ifaces[bluezDeviceIface]
		if !ok {
			continue
		}
		if !strings.HasPrefix(string(path), adapterPrefix) {
			continue
		}
		d := parseDevice(path, props, ifaces)
		if !d.Paired {
			continue
		}
		devices = append(devices, d)
	}

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Connected != devices[j].Connected {
			return devices[i].Connected
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})

	return devices, nil
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
	if c.verbose {
		fmt.Fprintf(os.Stderr, "[v] call: %s.%s on %s\n", iface, method, path)
	}

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
	name := "Unknown"
	if getObjectProperty(props, "Name", &name) && name == "" {
		name = "Unknown"
	}
	addr := ""
	getObjectProperty(props, "Address", &addr)
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

// batteryFromInterfaces reads Percentage from org.bluez.Battery1 when present.
func batteryFromInterfaces(ifaces map[string]map[string]dbus.Variant) int {
	if ifaces == nil {
		return batteryUnavailable
	}
	battProps, ok := ifaces[bluezBatteryIface]
	if !ok {
		return batteryUnavailable
	}
	var pct int
	if getObjectProperty(battProps, propPercentage, &pct) {
		return pct
	}
	return batteryUnavailable
}

// stringProp reads a string property with a fallback value.
func stringProp(props map[string]dbus.Variant, key, fallback string) string {
	var s string
	if getObjectProperty(props, key, &s) {
		return s
	}
	return fallback
}
