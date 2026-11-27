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

	"github.com/godbus/dbus/v5"
)

// managedObjects is the shape returned by ObjectManager.GetManagedObjects:
// path -> interface -> property -> variant.
type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Client wraps a system D-Bus connection used to talk to BlueZ.
type Client struct {
	conn            *dbus.Conn
	verbose         bool
	mu              sync.Mutex // guards activeAdapter and connectionLocks
	activeAdapter   dbus.ObjectPath
	connectionLocks map[dbus.ObjectPath]*sync.Mutex
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

// New opens a private connection to the system bus and verifies that org.bluez
// is reachable by issuing a GetManagedObjects probe on the root object. When
// verbose is true, each outgoing D-Bus method call is logged to stderr.
//
// The connection is private (not godbus's shared SystemBus) because Close
// tears it down; closing the shared connection would break any other user of
// it in the same process. DBUS_SYSTEM_BUS_ADDRESS is honoured, which is how the
// tests point the client at a fake bluetoothd.
func New(ctx context.Context, verbose bool) (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system D-Bus: %w", err)
	}
	c := &Client{conn: conn, verbose: verbose}
	if err := c.callCtx(ctx, bluezRootPath, dbusObjectManagerIface, "GetManagedObjects"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("probe BlueZ: %w", err)
	}
	adapters, err := c.ListAdapters(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("init bluetooth client: %w", err)
	}
	if len(adapters) > 0 {
		c.SetActiveAdapter(adapters[0].Path)
	}
	return c, nil
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
// Client is using. Returns nil after Close has run.
func (c *Client) Conn() *dbus.Conn {
	if c == nil {
		return nil
	}
	return c.conn
}

// isDBusError reports whether err (or any error it wraps) is a dbus.Error
// whose Name field matches name.
func isDBusError(err error, name string) bool {
	var dberr dbus.Error
	if errors.As(err, &dberr) {
		return dberr.Name == name
	}
	return false
}

// Close releases the underlying D-Bus connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close D-Bus connection: %w", err)
	}
	c.conn = nil
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
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	adapterPath := c.ActiveAdapter()
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
	if c == nil || c.conn == nil {
		return fmt.Errorf("dbus call: bluetooth client is not connected")
	}
	if c.verbose {
		fmt.Fprintf(os.Stderr, "[v] call: %s.%s on %s\n", iface, method, path)
	}

	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
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
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return "", nil, err
	}
	path := c.ActiveAdapter()
	if path == "" {
		return "", objects, fmt.Errorf("find adapter: %w", ErrNoAdapter)
	}
	ifaces, ok := objects[path]
	if !ok {
		return "", objects, fmt.Errorf("find adapter %s: %w", path, ErrAdapterNotReady)
	}
	if _, ok := ifaces[bluezAdapterIface]; !ok {
		return "", objects, fmt.Errorf("find adapter %s: missing %s", path, bluezAdapterIface)
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
