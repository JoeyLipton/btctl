// Package bluetooth - scanner.go runs BlueZ device discovery for a bounded
// duration and reports newly seen devices. It subscribes to InterfacesAdded and
// PropertiesChanged before calling StartDiscovery so no early signals are
// missed, deduplicates by address, and excludes devices BlueZ already knew
// about before the scan started. StopDiscovery always runs on return.
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
	// ScanEventFound fires the first time a brand-new device is observed.
	ScanEventFound ScanEventKind = iota
	// ScanEventUpdated fires when a device acquires a human-readable Name.
	ScanEventUpdated
)

// ScanEvent is delivered to the optional callback supplied to Scan.
type ScanEvent struct {
	Kind   ScanEventKind
	Device Device
}

type scannedDevice struct {
	device         Device
	emittedUpdated bool
}

// Scan runs Bluetooth discovery on the active adapter for the given duration.
func (c *Client) Scan(ctx context.Context, duration time.Duration, onEvent func(ScanEvent)) (devices []Device, retErr error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("scan: bluetooth client is not connected")
	}
	if duration <= 0 {
		return nil, fmt.Errorf("scan: duration must be positive, got %s", duration)
	}

	adapterPath, preObjects, err := c.findAdapterPath(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	cleanup, sigCh, err := c.subscribeScanSignals()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := c.callCtx(ctx, adapterPath, bluezAdapterIface, "StartDiscovery"); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	defer func() {
		if err := c.callCtx(context.Background(), adapterPath, bluezAdapterIface, "StopDiscovery"); err != nil && retErr == nil {
			retErr = fmt.Errorf("scan: %w", err)
		}
	}()

	preExisting := snapshotAddresses(preObjects)
	byAddress := make(map[string]*scannedDevice)
	pathToAddress := make(map[dbus.ObjectPath]string)

	emit := func(kind ScanEventKind, d Device) {
		if onEvent != nil {
			onEvent(ScanEvent{Kind: kind, Device: d})
		}
	}

	devices, retErr = c.runScanLoop(ctx, duration, sigCh, preExisting, byAddress, pathToAddress, emit)
	return devices, retErr
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

// runScanLoop processes D-Bus signals until duration elapses or ctx is canceled.
func (c *Client) runScanLoop(
	ctx context.Context,
	duration time.Duration,
	sigCh chan *dbus.Signal,
	preExisting map[string]bool,
	byAddress map[string]*scannedDevice,
	pathToAddress map[dbus.ObjectPath]string,
	emit func(ScanEventKind, Device),
) ([]Device, error) {
	timer := time.NewTimer(duration)
	defer timer.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-timer.C:
			break loop
		case sig := <-sigCh:
			if sig == nil {
				continue
			}
			switch sig.Name {
			case signalInterfacesAdded:
				path, props, ok := decodeInterfacesAdded(sig)
				if ok {
					scanHandleAdded(path, props, preExisting, byAddress, pathToAddress, emit)
				}
			case signalPropertiesChanged:
				iface, changed, ok := decodePropertiesChanged(sig)
				if ok {
					scanHandlePropsChanged(sig.Path, iface, changed, byAddress, pathToAddress, emit)
				}
			}
		}
	}

	return collectScanResults(byAddress), nil
}

// scanHandleAdded records a newly discovered device during scan.
func scanHandleAdded(
	path dbus.ObjectPath,
	props map[string]dbus.Variant,
	preExisting map[string]bool,
	byAddress map[string]*scannedDevice,
	pathToAddress map[dbus.ObjectPath]string,
	emit func(ScanEventKind, Device),
) {
	d := parseDevice(path, props, nil)
	key := strings.ToLower(d.Address)
	if key == "" || preExisting[key] {
		return
	}
	if deduplicateDevices(byAddress, pathToAddress, path, d) {
		emit(ScanEventFound, d)
	}
}

// scanHandlePropsChanged updates a device name from PropertiesChanged.
func scanHandlePropsChanged(
	path dbus.ObjectPath,
	iface string,
	changed map[string]dbus.Variant,
	byAddress map[string]*scannedDevice,
	pathToAddress map[dbus.ObjectPath]string,
	emit func(ScanEventKind, Device),
) {
	if iface != bluezDeviceIface {
		return
	}
	key, ok := pathToAddress[path]
	if !ok {
		return
	}
	state, ok := byAddress[key]
	if !ok || state.emittedUpdated || !isUnknownName(state.device.Name) {
		return
	}
	var name string
	if !getObjectProperty(changed, "Name", &name) || name == "" {
		return
	}
	state.device.Name = name
	state.emittedUpdated = true
	emit(ScanEventUpdated, state.device)
}

// deduplicateDevices merges incoming into seen by address. Returns true when
// incoming is a newly discovered device that should emit ScanEventFound.
func deduplicateDevices(
	seen map[string]*scannedDevice,
	pathToAddress map[dbus.ObjectPath]string,
	path dbus.ObjectPath,
	incoming Device,
) bool {
	key := strings.ToLower(incoming.Address)
	existing, found := seen[key]
	if !found {
		seen[key] = &scannedDevice{device: incoming}
		pathToAddress[path] = key
		return true
	}
	if isUnknownName(existing.device.Name) && !isUnknownName(incoming.Name) {
		existing.device.Name = incoming.Name
	}
	pathToAddress[path] = key
	return false
}

// collectScanResults builds a sorted device slice from the scan accumulator.
func collectScanResults(byAddress map[string]*scannedDevice) []Device {
	devices := make([]Device, 0, len(byAddress))
	for _, s := range byAddress {
		devices = append(devices, s.device)
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

// snapshotAddresses returns lower-cased device addresses present before scan.
func snapshotAddresses(objects managedObjects) map[string]bool {
	addrs := make(map[string]bool, len(objects))
	for _, ifaces := range objects {
		props, ok := ifaces[bluezDeviceIface]
		if !ok {
			continue
		}
		addr := stringProp(props, "Address", "")
		if addr != "" {
			addrs[strings.ToLower(addr)] = true
		}
	}
	return addrs
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
