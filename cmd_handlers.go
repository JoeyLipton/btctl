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

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/desktop"
	"github.com/joeylipton/btctl/internal/menu"
	"github.com/joeylipton/btctl/internal/textfmt"
)

func runList(ctx context.Context, client *bluez.Client, verbose bool) (int, error) {
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
		line := fmt.Sprintf("[%-9s]\t%s\t%s", d.Status(), d.DisplayName(), d.Address)
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
func runStatus(copts bluez.Options) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()

	client, err := bluez.New(copts)
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

	var connected []bluez.Device
	for _, d := range state.Devices {
		if d.Connected {
			connected = append(connected, d)
		}
	}
	fmt.Printf("[BT] %d\n", len(connected))
	for _, d := range connected {
		fmt.Printf("%s\t%s\n", d.DisplayName(), d.Address)
	}
	return exitSuccess, nil
}

func runScan(ctx context.Context, client *bluez.Client, seconds int) (int, error) {
	fmt.Printf("Scanning for %d seconds...\n", seconds)

	onEvent := func(ev bluez.ScanEvent) {
		switch ev.Kind {
		case bluez.ScanEventFound:
			fmt.Printf("found: %s  %s\n", ev.Device.DisplayName(), ev.Device.Address)
		case bluez.ScanEventUpdated:
			fmt.Printf("updated: %s  %s\n", ev.Device.Name, ev.Device.Address)
		}
	}

	devices, err := client.Scan(ctx, time.Duration(seconds)*time.Second, onEvent)
	if err != nil {
		return exitError, fmt.Errorf("scan: %w", err)
	}
	if ctx.Err() != nil {
		return exitInterrupted, nil
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
func lookupDevice(ctx context.Context, client *bluez.Client, address string) (bluez.Device, error) {
	d, err := client.FindDevice(ctx, address)
	if errors.Is(err, bluez.ErrDeviceNotFound) {
		return d, fmt.Errorf("%w: %s (see btctl list, or btctl scan to find new devices)", bluez.ErrDeviceNotFound, address)
	}
	return d, err
}

func runConnect(ctx context.Context, client *bluez.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if dev.Connected {
		fmt.Printf("%s (%s) is already connected\n", dev.DisplayName(), dev.Address)
		return exitSuccess, nil
	}
	return connectAndReport(ctx, client, dev)
}

// connectAndReport pairs (when needed) and connects dev. Progress goes to
// stderr, the result to stdout and a desktop notification (btctl is often
// run from a keybinding).
func connectAndReport(ctx context.Context, client *bluez.Client, dev bluez.Device) (int, error) {
	name := dev.DisplayName()
	result, err := client.Connect(ctx, dev.Path, func(stage bluez.ConnectStage) {
		switch stage {
		case bluez.StagePairing:
			fmt.Fprintf(os.Stderr, "[btctl] pairing with %s (%s)...\n", name, dev.Address)
		case bluez.StageConnecting:
			fmt.Fprintf(os.Stderr, "[btctl] connecting to %s (%s)...\n", name, dev.Address)
		}
	})
	if err != nil {
		desktop.Notify("Could not connect to " + name + ": " + bluez.FormatUserError(err))
		return exitError, err
	}
	msg := "Connected to " + name
	if result.BondingPending {
		msg += " (bonding may still be completing)"
	}
	fmt.Printf("%s (%s)\n", msg, dev.Address)
	desktop.Notify(msg)
	return exitSuccess, nil
}

func runPair(ctx context.Context, client *bluez.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	name := dev.DisplayName()
	if dev.Paired {
		fmt.Printf("%s (%s) is already paired\n", name, dev.Address)
		return exitSuccess, nil
	}
	fmt.Fprintf(os.Stderr, "[btctl] pairing with %s (%s)...\n", name, dev.Address)
	if err := client.Pair(ctx, dev.Path); err != nil {
		desktop.Notify("Could not pair with " + name + ": " + bluez.FormatUserError(err))
		return exitError, err
	}
	fmt.Printf("Paired with %s (%s)\n", name, dev.Address)
	desktop.Notify("Paired with " + name)
	return exitSuccess, nil
}

func runDisconnect(ctx context.Context, client *bluez.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if !dev.Connected {
		fmt.Printf("%s (%s) is not connected\n", dev.DisplayName(), dev.Address)
		return exitSuccess, nil
	}
	if err := client.Disconnect(ctx, dev.Path); err != nil {
		return exitError, err
	}
	fmt.Printf("Disconnected from %s (%s)\n", dev.DisplayName(), dev.Address)
	desktop.Notify("Disconnected from " + dev.DisplayName())
	return exitSuccess, nil
}

func runForget(ctx context.Context, client *bluez.Client, address string) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if err := client.ForgetDevice(ctx, dev.Path); err != nil {
		return exitError, err
	}
	fmt.Printf("Forgot %s (%s)\n", dev.DisplayName(), dev.Address)
	desktop.Notify("Removed " + dev.DisplayName())
	return exitSuccess, nil
}

func runTrust(ctx context.Context, client *bluez.Client, address string, trust bool) (int, error) {
	dev, err := lookupDevice(ctx, client, address)
	if err != nil {
		return exitError, err
	}
	if err := client.SetTrusted(ctx, dev.Path, trust); err != nil {
		return exitError, err
	}
	if trust {
		fmt.Printf("Trusted: %s (%s)\n", dev.DisplayName(), dev.Address)
	} else {
		fmt.Printf("Untrusted: %s (%s)\n", dev.DisplayName(), dev.Address)
	}
	return exitSuccess, nil
}

func runPick(ctx context.Context, m *menu.Menu) (int, error) {
	if err := m.Run(ctx); err != nil {
		return exitError, fmt.Errorf("pick: %w", err)
	}
	return exitSuccess, nil
}

func selectScanDevice(ctx context.Context, client *bluez.Client, devices []bluez.Device) (int, error) {
	fmt.Println("New devices found:")
	for i, d := range devices {
		name := textfmt.Truncate(d.DisplayName(), scanNameWidth)
		fmt.Printf("  %d) %-*s %s\n", i+1, scanNameWidth, name, d.Address)
	}
	fmt.Print("Enter number to connect (or press Enter to skip): ")

	line, err := readLine(ctx, os.Stdin)
	if err != nil {
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

// readLine reads one line from r, giving up when ctx is cancelled (the read
// itself cannot be interrupted, so it is left to finish in the background).
func readLine(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-ctx.Done():
		fmt.Println()
		return "", ctx.Err()
	}
}
