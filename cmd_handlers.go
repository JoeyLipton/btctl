package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joeylipton/btctl/bluetooth"

	"github.com/godbus/dbus/v5"
)

func runList(ctx context.Context, client *bluetooth.Client, verbose bool) (int, error) {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return exitError, fmt.Errorf("list devices: %w", err)
	}
	if len(devices) == 0 {
		fmt.Println("no paired devices found")
		return exitSuccess, nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, d := range devices {
		line := fmt.Sprintf("[%-9s]\t%s\t%s", deviceStatus(d), d.Name, d.Address)
		if d.Battery >= 0 {
			line += fmt.Sprintf("\t%d%%", d.Battery)
		}
		if d.RSSI != 0 {
			line += fmt.Sprintf("\t%ddBm", d.RSSI)
		}
		if verbose {
			line += fmt.Sprintf("\t%s", trustedLabel(d.Trusted))
		}
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return exitError, fmt.Errorf("flushing device table: %w", err)
	}
	return exitSuccess, nil
}

func trustedLabel(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

// runStatus owns its own client lifecycle so it never emits anything on
// stderr in normal (non-verbose) mode. It always exits 0.
func runStatus(verbose bool) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bluetooth.StatusClientTimeout())
	defer cancel()

	client, err := bluetooth.New(ctx, verbose)
	if err != nil {
		fmt.Println("[BT] off")
		return exitSuccess, nil
	}
	defer func() { _ = client.Close() }()

	powered, err := client.GetAdapterPowered(ctx)
	if err != nil || !powered {
		fmt.Println("[BT] off")
		return exitSuccess, nil
	}

	devices, err := client.ListDevices(ctx)
	if err != nil {
		fmt.Println("[BT] off")
		return exitSuccess, nil
	}

	var connected []bluetooth.Device
	for _, d := range devices {
		if d.Connected {
			connected = append(connected, d)
		}
	}

	fmt.Printf("[BT] %d\n", len(connected))
	for _, d := range connected {
		fmt.Printf("%s\t%s\n", displayName(d), d.Address)
	}
	return exitSuccess, nil
}

func runScan(ctx context.Context, client *bluetooth.Client, seconds int) (int, error) {
	fmt.Printf("Scanning for %d seconds...\n", seconds)

	onEvent := func(ev bluetooth.ScanEvent) {
		switch ev.Kind {
		case bluetooth.ScanEventFound:
			fmt.Printf("found: %s  %s\n", displayName(ev.Device), ev.Device.Address)
		case bluetooth.ScanEventUpdated:
			fmt.Printf("updated: %s  %s\n", ev.Device.Name, ev.Device.Address)
		}
	}

	devices, err := client.Scan(ctx, time.Duration(seconds)*time.Second, onEvent)
	if err != nil {
		return exitError, fmt.Errorf("scan: %w", err)
	}

	fmt.Println()
	fmt.Printf("Scan complete. %d new device(s) found.\n", len(devices))

	if len(devices) == 0 {
		fmt.Println("No new devices found.")
		return exitSuccess, nil
	}
	return selectScanDevice(ctx, client, devices)
}

func runConnect(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	path, err := resolveAddress(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	name := connectDisplayName(ctx, client, path, address)
	fmt.Fprintf(os.Stderr, "[btctl] connecting to %s (%s)...\n", name, address)
	if err := client.Connect(ctx, path); err != nil {
		if errors.Is(err, bluetooth.ErrConnectInProgress) {
			fmt.Fprintf(os.Stderr, "[btctl] already connecting to %s\n", address)
			return exitSuccess, nil
		}
		if connectTimedOut(err) {
			fmt.Fprintf(os.Stderr, "[btctl] error: connect timed out\n")
		}
		return exitError, err
	}
	fmt.Fprintf(os.Stderr, "[btctl] connected to %s (%s)\n", name, address)
	return exitSuccess, nil
}

func connectTimedOut(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline") ||
		strings.Contains(msg, "timed out")
}

func connectDisplayName(ctx context.Context, client *bluetooth.Client, path dbus.ObjectPath, address string) string {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return address
	}
	for _, d := range devices {
		if d.Path == path {
			return displayName(d)
		}
	}
	return address
}

func runDisconnect(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	path, err := resolveAddress(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if err := client.Disconnect(ctx, path); err != nil {
		return exitError, err
	}
	return exitSuccess, nil
}

func runForget(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return exitError, fmt.Errorf("forget: %w", err)
	}
	needle := strings.ToLower(address)
	var target *bluetooth.Device
	for i := range devices {
		if strings.ToLower(devices[i].Address) == needle {
			target = &devices[i]
			break
		}
	}
	if target == nil {
		return exitError, fmt.Errorf("forget device %s: %w", address, bluetooth.ErrDeviceNotFound)
	}
	if err := client.ForgetDevice(ctx, target.Path); err != nil {
		return exitError, err
	}
	name := target.Name
	if name == "" {
		name = address
	}
	fmt.Printf("Forgot %s (%s)\n", name, address)
	return exitSuccess, nil
}

func runTrust(ctx context.Context, client *bluetooth.Client, address string, trust bool) (int, error) {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return exitError, fmt.Errorf("trust: %w", err)
	}
	needle := strings.ToLower(address)
	var target *bluetooth.Device
	for i := range devices {
		if strings.ToLower(devices[i].Address) == needle {
			target = &devices[i]
			break
		}
	}
	if target == nil {
		return exitError, fmt.Errorf("trust device %s: %w", address, bluetooth.ErrDeviceNotFound)
	}
	if err := client.SetTrusted(ctx, target.Path, trust); err != nil {
		return exitError, err
	}
	name := displayName(*target)
	if trust {
		fmt.Printf("Trusted: %s (%s)\n", name, address)
	} else {
		fmt.Printf("Untrusted: %s (%s)\n", name, address)
	}
	return exitSuccess, nil
}

func runPick(_ context.Context, client *bluetooth.Client, cfg bluetooth.PickerConfig, agent *bluetooth.Agent) (int, error) {
	menu := bluetooth.NewMenu(client, cfg, agent)
	if err := menu.Run(); err != nil {
		return exitError, fmt.Errorf("pick: %w", err)
	}
	return exitSuccess, nil
}

func selectScanDevice(ctx context.Context, client *bluetooth.Client, devices []bluetooth.Device) (int, error) {
	fmt.Println("New devices found:")
	for i, d := range devices {
		name := displayName(d)
		if len(name) > scanNameWidth {
			name = name[:scanNameWidth]
		}
		fmt.Printf("  %d) %-*s %s\n", i+1, scanNameWidth, name, d.Address)
	}
	fmt.Print("Enter number to connect (or press Enter to skip): ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return exitError, fmt.Errorf("reading prompt input: %w", err)
	}
	raw := strings.TrimSpace(line)
	if raw == "" {
		return exitSuccess, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid selection: %q\n", raw)
		return exitError, nil
	}
	if n < 1 || n > len(devices) {
		fmt.Fprintf(os.Stderr, "selection out of range: %d\n", n)
		return exitError, nil
	}

	target := devices[n-1]
	if err := client.Connect(ctx, target.Path); err != nil {
		return exitError, err
	}
	fmt.Printf("Connected to %s (%s)\n", displayName(target), target.Address)
	return exitSuccess, nil
}

func resolveAddress(ctx context.Context, client *bluetooth.Client, address string) (dbus.ObjectPath, error) {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up %q: %w", address, err)
	}
	needle := strings.ToLower(address)
	for _, d := range devices {
		if strings.ToLower(d.Address) == needle {
			return d.Path, nil
		}
	}
	return "", fmt.Errorf("lookup device %s: %w", address, bluetooth.ErrDeviceNotFound)
}

func deviceStatus(d bluetooth.Device) string {
	switch {
	case d.Connected:
		return "connected"
	case d.Paired:
		return "paired"
	default:
		return ""
	}
}

func displayName(d bluetooth.Device) string {
	if d.Name == "" || d.Name == "Unknown" {
		return "(unknown)"
	}
	return d.Name
}
