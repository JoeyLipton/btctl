// Package bluetooth - scanner.go runs BlueZ device discovery for a bounded
// duration and reports devices in range that are not paired yet.
//
// It subscribes to InterfacesAdded and PropertiesChanged before taking the
// object snapshot and calling StartDiscovery, so no device can slip between
// the two. Results are deduplicated by address and limited to the active
// adapter. Paired devices are never results (they are listed elsewhere), but
// unpaired devices BlueZ already had cached are: BlueZ keeps recently seen
// devices for a while (30 s by default, longer while another tool scans), so
// such a device is reported as soon as it is heard during the scan. Discovery
// is always stopped on return.
package bluetooth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ScanEventKind classifies events emitted while a Scan is in progress.
type ScanEventKind int

const (
	// ScanEventFound fires the first time a device is seen during the scan.
	ScanEventFound ScanEventKind = iota
	// ScanEventUpdated fires when a found device acquires a human-readable Name.
	ScanEventUpdated
)

// ScanEvent is delivered to the optional callback supplied to Scan.
type ScanEvent struct {
	Kind   ScanEventKind
	Device Device
}

// advertisementProps are Device1 properties BlueZ only updates when it
// receives an advertisement or inquiry result, i.e. when the device is in range.
var advertisementProps = []string{"RSSI", "TxPower", "ManufacturerData", "ServiceData"}

// Scan runs Bluetooth discovery on the active adapter for the given duration.
func (c *Client) Scan(ctx context.Context, duration time.Duration, onEvent func(ScanEvent)) (devices []Device, retErr error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("scan: bluetooth client is not connected")
	}
	if duration <= 0 {
		return nil, fmt.Errorf("scan: duration must be positive, got %s", duration)
	}

	cleanup, sigCh, err := c.subscribeScanSignals()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	adapterPath, objects, err := c.findAdapterPath(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	if err := c.callCtx(ctx, adapterPath, bluezAdapterIface, "StartDiscovery"); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	defer func() {
		if err := c.callCtx(context.Background(), adapterPath, bluezAdapterIface, "StopDiscovery"); err != nil && retErr == nil {
			retErr = fmt.Errorf("scan: %w", err)
		}
	}()

	state := newScanState(adapterPath, objects, onEvent)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return state.results(), nil
		case <-timer.C:
			return state.results(), nil
		case sig := <-sigCh:
			state.handle(sig)
		}
	}
}

// subscribeScanSignals registers D-Bus match rules for scan events.
func (c *Client) subscribeScanSignals() (cleanup func(), sigCh chan *dbus.Signal, err error) {
	addedRule := []dbus.MatchOption{
		dbus.WithMatchSender(bluezService),
		dbus.WithMatchObjectPath(bluezRootPath),
		dbus.WithMatchInterface(dbusObjectManagerIface),
		dbus.WithMatchMember(signalMemberInterfacesAdded),
	}
	if err := c.conn.AddMatchSignal(addedRule...); err != nil {
		return nil, nil, fmt.Errorf("scan: subscribe InterfacesAdded: %w", err)
	}

	propsRule := []dbus.MatchOption{
		dbus.WithMatchSender(bluezService),
		dbus.WithMatchInterface(dbusPropertiesIface),
		dbus.WithMatchMember(signalMemberPropertiesChanged),
	}
	if err := c.conn.AddMatchSignal(propsRule...); err != nil {
		_ = c.conn.RemoveMatchSignal(addedRule...)
		return nil, nil, fmt.Errorf("scan: subscribe PropertiesChanged: %w", err)
	}

	sigCh = make(chan *dbus.Signal, signalChannelBuffer)
	c.conn.Signal(sigCh)

	cleanup = func() {
		_ = c.conn.RemoveMatchSignal(addedRule...)
		_ = c.conn.RemoveMatchSignal(propsRule...)
		c.conn.RemoveSignal(sigCh)
	}
	return cleanup, sigCh, nil
}

// scanState accumulates scan results.
type scanState struct {
	adapterPrefix string                     // "/org/bluez/hci0/"
	paired        map[string]bool            // lower-cased addresses of paired devices
	cached        map[dbus.ObjectPath]Device // unpaired devices known before the scan, not yet heard
	found         map[string]*scannedDevice  // results by lower-cased address
	pathToAddress map[dbus.ObjectPath]string
	onEvent       func(ScanEvent)
}

type scannedDevice struct {
	device         Device
	emittedUpdated bool
}

func newScanState(adapterPath dbus.ObjectPath, objects managedObjects, onEvent func(ScanEvent)) *scanState {
	s := &scanState{
		adapterPrefix: string(adapterPath) + "/",
		paired:        make(map[string]bool),
		cached:        make(map[dbus.ObjectPath]Device),
		found:         make(map[string]*scannedDevice),
		pathToAddress: make(map[dbus.ObjectPath]string),
		onEvent:       onEvent,
	}
	for path, ifaces := range objects {
		props, ok := ifaces[bluezDeviceIface]
		if !ok {
			continue
		}
		d := parseDevice(path, props, ifaces)
		if d.Address == "" {
			continue
		}
		if d.Paired {
			s.paired[strings.ToLower(d.Address)] = true
		} else if s.onAdapter(path) {
			s.cached[path] = d
		}
	}
	return s
}

func (s *scanState) onAdapter(path dbus.ObjectPath) bool {
	return strings.HasPrefix(string(path), s.adapterPrefix)
}

func (s *scanState) emit(kind ScanEventKind, d Device) {
	if s.onEvent != nil {
		s.onEvent(ScanEvent{Kind: kind, Device: d})
	}
}

// handle processes one D-Bus signal.
func (s *scanState) handle(sig *dbus.Signal) {
	if sig == nil {
		return
	}
	switch sig.Name {
	case signalInterfacesAdded:
		if path, props, ok := decodeInterfacesAdded(sig); ok && s.onAdapter(path) {
			s.add(path, parseDevice(path, props, nil))
		}
	case signalPropertiesChanged:
		if iface, changed, ok := decodePropertiesChanged(sig); ok && iface == bluezDeviceIface {
			s.propertiesChanged(sig.Path, changed)
		}
	}
}

// add records a device seen during the scan, ignoring paired devices.
func (s *scanState) add(path dbus.ObjectPath, d Device) {
	key := strings.ToLower(d.Address)
	if key == "" || d.Paired || s.paired[key] {
		return
	}
	s.pathToAddress[path] = key
	if existing, ok := s.found[key]; ok {
		if isUnknownName(existing.device.Name) && !isUnknownName(d.Name) {
			existing.device.Name = d.Name
		}
		return
	}
	s.found[key] = &scannedDevice{device: d}
	s.emit(ScanEventFound, d)
}

// propertiesChanged reports cached devices once they advertise and picks up
// names that arrive after a device was found.
func (s *scanState) propertiesChanged(path dbus.ObjectPath, changed map[string]dbus.Variant) {
	if d, ok := s.cached[path]; ok {
		if !hasAny(changed, advertisementProps) {
			return
		}
		delete(s.cached, path)
		var name string
		if getObjectProperty(changed, "Name", &name) && name != "" {
			d.Name = name
		}
		var rssi int16
		if getObjectProperty(changed, "RSSI", &rssi) {
			d.RSSI = rssi
		}
		s.add(path, d)
		return
	}

	key, ok := s.pathToAddress[path]
	if !ok {
		return
	}
	state, ok := s.found[key]
	if !ok || state.emittedUpdated || !isUnknownName(state.device.Name) {
		return
	}
	var name string
	if !getObjectProperty(changed, "Name", &name) || name == "" {
		return
	}
	state.device.Name = name
	state.emittedUpdated = true
	s.emit(ScanEventUpdated, state.device)
}

// results returns the found devices: named ones first (alphabetically), then
// unnamed ones by address.
func (s *scanState) results() []Device {
	devices := make([]Device, 0, len(s.found))
	for _, f := range s.found {
		devices = append(devices, f.device)
	}
	sort.Slice(devices, func(i, j int) bool {
		iUnknown := isUnknownName(devices[i].Name)
		jUnknown := isUnknownName(devices[j].Name)
		if iUnknown != jUnknown {
			return !iUnknown
		}
		if iUnknown {
			return devices[i].Address < devices[j].Address
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})
	return devices
}

func hasAny(m map[string]dbus.Variant, keys []string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// decodeInterfacesAdded extracts the device payload of an InterfacesAdded signal.
func decodeInterfacesAdded(sig *dbus.Signal) (dbus.ObjectPath, map[string]dbus.Variant, bool) {
	if len(sig.Body) < 2 {
		return "", nil, false
	}
	path, ok := sig.Body[0].(dbus.ObjectPath)
	if !ok {
		return "", nil, false
	}
	ifaces, ok := sig.Body[1].(map[string]map[string]dbus.Variant)
	if !ok {
		return "", nil, false
	}
	props, ok := ifaces[bluezDeviceIface]
	if !ok {
		return "", nil, false
	}
	return path, props, true
}

// decodePropertiesChanged extracts interface and changed props from a signal.
func decodePropertiesChanged(sig *dbus.Signal) (string, map[string]dbus.Variant, bool) {
	if len(sig.Body) < 2 {
		return "", nil, false
	}
	iface, ok := sig.Body[0].(string)
	if !ok {
		return "", nil, false
	}
	changed, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return "", nil, false
	}
	return iface, changed, true
}

func isUnknownName(name string) bool {
	return name == "" || name == "Unknown"
}
