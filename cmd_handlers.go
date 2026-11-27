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
	"github.com/joeylipton/btctl/internal/textfmt"
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

// runStatus owns its own client lifecycle so it never writes to stderr unless
// -v was given, which keeps status bars clean. It always exits 0.
func runStatus(copts bluetooth.Options) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bluetooth.StatusClientTimeout())
	defer cancel()

	client, err := bluetooth.New(copts)
	if err != nil {
		fmt.Println("[BT] off")
		return exitSuccess, nil
	}
	defer func() { _ = client.Close() }()

	state, err := client.State(ctx)
	if err != nil || !state.Found || !state.Adapter.Powered {
		fmt.Println("[BT] off")
		return exitSuccess, nil
	}

	var connected []bluetooth.Device
	for _, d := range state.Devices {
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

// lookupDevice finds a device BlueZ knows on the active adapter by address.
func lookupDevice(ctx context.Context, client *bluetooth.Client, address string) (bluetooth.Device, error) {
	d, err := client.FindDevice(ctx, address)
	if errors.Is(err, bluetooth.ErrDeviceNotFound) {
		return d, fmt.Errorf("%w: %s (see btctl list, or btctl scan to find new devices)", bluetooth.ErrDeviceNotFound, address)
	}
	return d, err
}

func runConnect(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if dev.Connected {
		fmt.Printf("%s (%s) is already connected\n", displayName(dev), dev.Address)
		return exitSuccess, nil
	}
	return connectAndReport(ctx, client, dev)
}

// connectAndReport pairs (when needed) and connects dev. Progress goes to
// stderr, the result to stdout and a desktop notification (btctl is often
// run from a keybinding).
func connectAndReport(ctx context.Context, client *bluetooth.Client, dev bluetooth.Device) (int, error) {
	name := displayName(dev)
	result, err := client.Connect(ctx, dev.Path, func(stage bluetooth.ConnectStage) {
		switch stage {
		case bluetooth.StagePairing:
			fmt.Fprintf(os.Stderr, "[btctl] pairing with %s (%s)...\n", name, dev.Address)
		case bluetooth.StageConnecting:
			fmt.Fprintf(os.Stderr, "[btctl] connecting to %s (%s)...\n", name, dev.Address)
		}
	})
	if err != nil {
		bluetooth.Notify("Could not connect to " + name + ": " + bluetooth.FormatUserError(err))
		return exitError, err
	}
	msg := "Connected to " + name
	if result.BondingPending {
		msg += " (bonding may still be completing)"
	}
	fmt.Printf("%s (%s)\n", msg, dev.Address)
	bluetooth.Notify(msg)
	return exitSuccess, nil
}

func runPair(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	name := displayName(dev)
	if dev.Paired {
		fmt.Printf("%s (%s) is already paired\n", name, dev.Address)
		return exitSuccess, nil
	}
	fmt.Fprintf(os.Stderr, "[btctl] pairing with %s (%s)...\n", name, dev.Address)
	if err := client.Pair(ctx, dev.Path); err != nil {
		bluetooth.Notify("Could not pair with " + name + ": " + bluetooth.FormatUserError(err))
		return exitError, err
	}
	fmt.Printf("Paired with %s (%s)\n", name, dev.Address)
	bluetooth.Notify("Paired with " + name)
	return exitSuccess, nil
}

func runDisconnect(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if !dev.Connected {
		fmt.Printf("%s (%s) is not connected\n", displayName(dev), dev.Address)
		return exitSuccess, nil
	}
	if err := client.Disconnect(ctx, dev.Path); err != nil {
		return exitError, err
	}
	fmt.Printf("Disconnected from %s (%s)\n", displayName(dev), dev.Address)
	bluetooth.Notify("Disconnected from " + displayName(dev))
	return exitSuccess, nil
}

func runForget(ctx context.Context, client *bluetooth.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if err := client.ForgetDevice(ctx, dev.Path); err != nil {
		return exitError, err
	}
	fmt.Printf("Forgot %s (%s)\n", displayName(dev), dev.Address)
	bluetooth.Notify("Removed " + displayName(dev))
	return exitSuccess, nil
}

func runTrust(ctx context.Context, client *bluetooth.Client, address string, trust bool) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if err := client.SetTrusted(ctx, dev.Path, trust); err != nil {
		return exitError, err
	}
	if trust {
		fmt.Printf("Trusted: %s (%s)\n", displayName(dev), dev.Address)
	} else {
		fmt.Printf("Untrusted: %s (%s)\n", displayName(dev), dev.Address)
	}
	return exitSuccess, nil
}

func runPick(menu *bluetooth.Menu) (int, error) {
	if err := menu.Run(); err != nil {
		return exitError, fmt.Errorf("pick: %w", err)
	}
	return exitSuccess, nil
}

func selectScanDevice(ctx context.Context, client *bluetooth.Client, devices []bluetooth.Device) (int, error) {
	fmt.Println("New devices found:")
	for i, d := range devices {
		name := textfmt.Truncate(displayName(d), scanNameWidth)
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

	return connectAndReport(ctx, client, devices[n-1])
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
