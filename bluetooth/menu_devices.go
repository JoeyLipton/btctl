// Package bluetooth - menu_devices.go implements device-focused sub-menus.
package bluetooth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// menuConnect presents the disconnected devices and connects to the chosen
// one. Errors from Connect are shown to the user but are not fatal.
func (m *Menu) menuConnect() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("menu connect: list devices: %w", err)
	}
	var disconnected []Device
	for _, d := range devices {
		if !d.Connected {
			disconnected = append(disconnected, d)
		}
	}
	if len(disconnected) == 0 {
		tell("No disconnected devices to connect")
		return nil
	}

	choice, err := m.prompt("Connect: select device", formatDeviceLines(disconnected))
	if err != nil {
		return fmt.Errorf("menu connect: prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	selected := matchDeviceByAddress(disconnected, choice)
	if selected == nil {
		return nil
	}

	return m.connectWithConfirmation(ctx, *selected)
}

// menuDisconnect presents the currently connected devices and disconnects
// the chosen one. Errors from Disconnect are shown to the user but are not
// fatal.
func (m *Menu) menuDisconnect() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("menu disconnect: list devices: %w", err)
	}
	var connected []Device
	for _, d := range devices {
		if d.Connected {
			connected = append(connected, d)
		}
	}
	if len(connected) == 0 {
		tell("No connected devices")
		return nil
	}

	choice, err := m.prompt("Disconnect: select device", formatDeviceLines(connected))
	if err != nil {
		return fmt.Errorf("menu disconnect: prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	selected := matchDeviceByAddress(connected, choice)
	if selected == nil {
		return nil
	}

	if err := m.client.Disconnect(ctx, selected.Path); err != nil {
		tell("Could not disconnect %s: %s", devicePresentName(*selected), FormatUserError(err))
		return nil
	}
	menuStderr("disconnected from %s", devicePresentName(*selected))
	return nil
}

// menuForget presents every known device, asks for explicit yes/no
// confirmation, then removes the device via Adapter1.RemoveDevice.
func (m *Menu) menuForget() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("menu forget: list devices: %w", err)
	}
	if len(devices) == 0 {
		tell("No paired devices")
		return nil
	}

	choice, err := m.prompt("Forget: select device to remove", formatDeviceLines(devices))
	if err != nil {
		return fmt.Errorf("menu forget: prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	selected := matchDeviceByAddress(devices, choice)
	if selected == nil {
		return nil
	}

	confirmTitle := fmt.Sprintf("Forget %s (%s) ?", devicePresentName(*selected), selected.Address)
	// Static prompt line, not a device line; buildDeviceLine does not apply here.
	confirm, err := m.prompt(confirmTitle, []string{
		"yes - remove this device",
		"no  - cancel",
	})
	if err != nil {
		return fmt.Errorf("menu forget: confirm: %w", err)
	}
	if confirm == "" || !strings.HasPrefix(confirm, "yes") {
		return nil
	}

	if err := m.client.ForgetDevice(ctx, selected.Path); err != nil {
		tell("Could not forget %s: %s", devicePresentName(*selected), FormatUserError(err))
		return nil
	}
	menuStderr("forgot %s", devicePresentName(*selected))
	return nil
}

// menuScan runs the full scan sub-menu in three phases: duration picker,
// progress display (notify-send, zenity, or stderr), then results picker.
func (m *Menu) menuScan() error {
	// Static prompt lines, not device lines; buildDeviceLine does not apply here.
	durations := []string{
		" 5 seconds",
		"10 seconds",
		"20 seconds",
		"30 seconds",
		"60 seconds",
	}
	choice, err := m.prompt("Scan: select duration", durations)
	if err != nil {
		return fmt.Errorf("menu scan: duration prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	seconds, err := parseScanDurationLine(choice)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := m.runScan(ctx, seconds)
	if err != nil {
		tell("Scan failed: %s", FormatUserError(err))
		return nil
	}

	if len(res) == 0 {
		// Static prompt line, not a device line; buildDeviceLine does not apply here.
		_, _ = m.prompt("Scan results", []string{"No new devices found   (Escape to go back)"})
		return nil
	}

	lines := make([]string, len(res))
	for i, d := range res {
		lines[i] = buildDeviceLine(d, i+1, true)
	}
	title := fmt.Sprintf("Scan: %d found - select to connect", len(res))
	selection, err := m.prompt(title, lines)
	if err != nil {
		return fmt.Errorf("menu scan: select prompt: %w", err)
	}
	if selection == "" {
		return nil
	}
	selected := matchDeviceByAddress(res, selection)
	if selected == nil {
		return nil
	}

	return m.connectWithConfirmation(ctx, *selected)
}

// runScan executes Client.Scan with a progress display and joins the scan
// goroutine before returning.
func (m *Menu) runScan(ctx context.Context, seconds int) ([]Device, error) {
	progress := newScanProgress(m.client.Verbose())
	progress.Start(seconds)

	type scanResult struct {
		devices []Device
		err     error
	}
	resultCh := make(chan scanResult, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		onEvent := func(ev ScanEvent) {
			if ev.Kind == ScanEventFound {
				progress.DeviceFound(devicePresentName(ev.Device), ev.Device.Address)
			}
		}
		devs, scanErr := m.client.Scan(ctx, time.Duration(seconds)*time.Second, onEvent)
		resultCh <- scanResult{devices: devs, err: scanErr}
	}()

	res := <-resultCh
	wg.Wait()
	progress.Complete(len(res.devices))

	if res.err != nil {
		return nil, res.err
	}
	return res.devices, nil
}

// menuInfo shows read-only properties for a selected device. The info
// dialog itself is also a prompt: any selection or cancel returns to the
// main menu.
func (m *Menu) menuInfo() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := m.client.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("menu info: list devices: %w", err)
	}
	if len(devices) == 0 {
		tell("No paired devices")
		return nil
	}

	choice, err := m.prompt("Info: select device", formatDeviceLines(devices))
	if err != nil {
		return fmt.Errorf("menu info: prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	selected := matchDeviceByAddress(devices, choice)
	if selected == nil {
		return nil
	}

	return m.showDeviceInfo(ctx, selected.Path)
}

// showDeviceInfo displays read-only device properties plus trust/untrust
// actions. Selecting an action updates Trusted via D-Bus, prints the
// outcome on stderr, and re-opens the screen with refreshed properties.
func (m *Menu) showDeviceInfo(ctx context.Context, path dbus.ObjectPath) error {
	for {
		fresh, err := refreshDevice(ctx, m.client, path)
		if err != nil {
			tell("Could not read the device: %s", FormatUserError(err))
			return nil
		}

		infoLines := []string{
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Name", devicePresentName(*fresh)),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Address", fresh.Address),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Connected", yesNo(fresh.Connected)),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Paired", yesNo(fresh.Paired)),
			fmt.Sprintf("%-*s %s", infoLabelWidth, "Trusted", yesNo(fresh.Trusted)),
		}
		if fresh.Battery > batteryUnavailable {
			infoLines = append(infoLines, fmt.Sprintf("%-*s %d%%", infoLabelWidth, "Battery", fresh.Battery))
		}
		if fresh.RSSI != 0 {
			infoLines = append(infoLines, fmt.Sprintf("%-*s %d dBm", infoLabelWidth, "RSSI", fresh.RSSI))
		}
		// Static prompt lines, not device lines; buildDeviceLine does not apply here.
		infoLines = append(infoLines, trustActionLine, untrustActionLine)

		choice, err := m.prompt("Device info", infoLines)
		if err != nil {
			return fmt.Errorf("menu info: device info prompt: %w", err)
		}
		if choice == "" {
			return nil
		}

		name := devicePresentName(*fresh)
		addr := fresh.Address
		switch choice {
		case trustActionLine:
			if err := m.client.SetTrusted(ctx, path, true); err != nil {
				tell("Could not trust %s: %s", name, FormatUserError(err))
			} else {
				tell("Trusted %s (%s)", name, addr)
			}
		case untrustActionLine:
			if err := m.client.SetTrusted(ctx, path, false); err != nil {
				tell("Could not untrust %s: %s", name, FormatUserError(err))
			} else {
				tell("Untrusted %s (%s)", name, addr)
			}
		default:
			continue
		}
	}
}

// refreshDevice re-reads the current snapshot of a device via ListDevices
// and returns the entry whose object path matches path.
func refreshDevice(ctx context.Context, c *Client, path dbus.ObjectPath) (*Device, error) {
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		if devices[i].Path == path {
			return &devices[i], nil
		}
	}
	return nil, fmt.Errorf("refresh device %s: %w", path, ErrDeviceNotFound)
}

// parseScanDurationLine extracts the integer number of seconds from a line
// emitted by the duration menu (e.g. " 5 seconds", "10 seconds").
func parseScanDurationLine(line string) (int, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return 0, fmt.Errorf("empty duration line")
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("non-positive duration: %d", n)
	}
	return n, nil
}
