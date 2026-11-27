// Package menu - devices.go implements the device sub-menus.
package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/desktop"
	"github.com/joeylipton/btctl/internal/textfmt"
)

// Static lines of the device info screen.
const (
	trustActionLine   = "> trust this device"
	untrustActionLine = "> untrust this device"
)

// scanDurations are the choices of the scan sub-menu. The user can also type
// a number of seconds.
var scanDurations = []string{" 5 seconds", "10 seconds", "20 seconds", "30 seconds", "60 seconds"}

// pickDevice lists the paired devices that match keep and lets the user
// choose one. ok is false when there was nothing to choose from (reported
// with emptyMsg) or the user dismissed the picker.
func (m *Menu) pickDevice(ctx context.Context, title, emptyMsg string, keep func(bluez.Device) bool) (bluez.Device, bool, error) {
	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return bluez.Device{}, false, fmt.Errorf("%s: %w", title, err)
	}
	var candidates []bluez.Device
	var lines []string
	for _, d := range devices {
		if keep == nil || keep(d) {
			candidates = append(candidates, d)
			lines = append(lines, deviceLine(d, 0))
		}
	}
	if len(candidates) == 0 {
		m.tell("%s", emptyMsg)
		return bluez.Device{}, false, nil
	}
	choice, err := m.prompt(ctx, title, lines)
	if err != nil || choice == "" {
		return bluez.Device{}, false, err
	}
	d, ok := deviceForLine(candidates, choice)
	return d, ok, nil
}

// menuConnect connects one of the disconnected paired devices.
func (m *Menu) menuConnect(ctx context.Context) error {
	d, ok, err := m.pickDevice(ctx, "Connect: select device", "No disconnected devices to connect",
		func(d bluez.Device) bool { return !d.Connected })
	if err != nil || !ok {
		return err
	}
	m.connect(ctx, d)
	return nil
}

// menuDisconnect disconnects one of the connected devices.
func (m *Menu) menuDisconnect(ctx context.Context) error {
	d, ok, err := m.pickDevice(ctx, "Disconnect: select device", "No connected devices",
		func(d bluez.Device) bool { return d.Connected })
	if err != nil || !ok {
		return err
	}
	if err := m.client.Disconnect(ctx, d.Path); err != nil {
		m.tell("Could not disconnect %s: %s", d.DisplayName(), bluez.FormatUserError(err))
		return nil
	}
	m.tell("Disconnected from %s", d.DisplayName())
	return nil
}

// menuForget removes a paired device after an explicit confirmation.
func (m *Menu) menuForget(ctx context.Context) error {
	d, ok, err := m.pickDevice(ctx, "Forget: select device to remove", "No paired devices", nil)
	if err != nil || !ok {
		return err
	}
	title := fmt.Sprintf("Forget %s (%s)?", d.DisplayName(), d.Address)
	confirm, err := m.prompt(ctx, title, []string{"yes - remove this device", "no  - cancel"})
	if err != nil || !strings.HasPrefix(confirm, "yes") {
		return err
	}
	if err := m.client.ForgetDevice(ctx, d.Path); err != nil {
		m.tell("Could not forget %s: %s", d.DisplayName(), bluez.FormatUserError(err))
		return nil
	}
	m.tell("Forgot %s", d.DisplayName())
	return nil
}

// menuScan asks for a duration, scans with progress feedback, and offers to
// connect one of the devices found.
func (m *Menu) menuScan(ctx context.Context) error {
	choice, err := m.prompt(ctx, "Scan: select duration", scanDurations)
	if err != nil || choice == "" {
		return err
	}
	seconds, err := parseScanDuration(choice)
	if err != nil {
		m.tell("Invalid scan duration: %s", err)
		return nil
	}

	found, err := m.scan(ctx, seconds)
	if err != nil {
		m.tell("Scan failed: %s", bluez.FormatUserError(err))
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if len(found) == 0 {
		_, err := m.prompt(ctx, "Scan results", []string{"No new devices found   (Escape to go back)"})
		return err
	}

	lines := make([]string, len(found))
	for i, d := range found {
		lines[i] = deviceLine(d, i+1)
	}
	selection, err := m.prompt(ctx, fmt.Sprintf("Scan: %d found - select to connect", len(found)), lines)
	if err != nil || selection == "" {
		return err
	}
	if d, ok := deviceForLine(found, selection); ok {
		m.connect(ctx, d)
	}
	return nil
}

// scan runs discovery for seconds while showing progress outside the picker.
func (m *Menu) scan(ctx context.Context, seconds int) ([]bluez.Device, error) {
	progress := desktop.NewProgress(m.client.Verbose())
	progress.Start(seconds)
	found, err := m.client.Scan(ctx, time.Duration(seconds)*time.Second, func(ev bluez.ScanEvent) {
		if ev.Kind == bluez.ScanEventFound {
			progress.DeviceFound(ev.Device.DisplayName(), ev.Device.Address)
		}
	})
	progress.Complete(len(found))
	return found, err
}

// parseScanDuration reads the seconds from a duration line (" 5 seconds") or
// a number the user typed, within the limits `btctl scan -t` accepts.
func parseScanDuration(line string) (int, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("%q is not a number of seconds", fields[0])
	}
	if d := time.Duration(n) * time.Second; d < bluez.MinScanDuration || d > bluez.MaxScanDuration {
		return 0, fmt.Errorf("%d seconds is outside %s-%s", n, bluez.MinScanDuration, bluez.MaxScanDuration)
	}
	return n, nil
}

// menuInfo shows the properties of a paired device, with trust actions.
func (m *Menu) menuInfo(ctx context.Context) error {
	d, ok, err := m.pickDevice(ctx, "Info: select device", "No paired devices", nil)
	if err != nil || !ok {
		return err
	}
	return m.showDeviceInfo(ctx, d.Path)
}

// showDeviceInfo displays read-only device properties plus trust/untrust
// actions. After an action the screen re-opens with refreshed properties.
func (m *Menu) showDeviceInfo(ctx context.Context, path dbus.ObjectPath) error {
	for ctx.Err() == nil {
		d, err := m.refreshDevice(ctx, path)
		if err != nil {
			m.tell("Could not read the device: %s", bluez.FormatUserError(err))
			return nil
		}
		lines := []string{
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Name", d.DisplayName()),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Address", d.Address),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Connected", textfmt.YesNo(d.Connected)),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Paired", textfmt.YesNo(d.Paired)),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Trusted", textfmt.YesNo(d.Trusted)),
		}
		if d.Battery != bluez.BatteryUnavailable {
			lines = append(lines, fmt.Sprintf("%-*s %d%%", infoLabelWidth, "Battery", d.Battery))
		}
		if d.RSSI != 0 {
			lines = append(lines, fmt.Sprintf("%-*s %d dBm", infoLabelWidth, "RSSI", d.RSSI))
		}
		lines = append(lines, trustActionLine, untrustActionLine)

		choice, err := m.prompt(ctx, "Device info", lines)
		if err != nil || choice == "" {
			return err
		}
		var trust bool
		switch choice {
		case trustActionLine:
			trust = true
		case untrustActionLine:
			trust = false
		default:
			continue
		}
		if err := m.client.SetTrusted(ctx, path, trust); err != nil {
			m.tell("Could not change trust for %s: %s", d.DisplayName(), bluez.FormatUserError(err))
		} else if trust {
			m.tell("Trusted %s (%s)", d.DisplayName(), d.Address)
		} else {
			m.tell("Untrusted %s (%s)", d.DisplayName(), d.Address)
		}
	}
	return nil
}

// refreshDevice re-reads the paired device at path.
func (m *Menu) refreshDevice(ctx context.Context, path dbus.ObjectPath) (bluez.Device, error) {
	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return bluez.Device{}, err
	}
	for _, d := range devices {
		if d.Path == path {
			return d, nil
		}
	}
	return bluez.Device{}, fmt.Errorf("%w: %s", bluez.ErrDeviceNotFound, path)
}
