package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/joeylipton/btctl/bluetooth"
	"github.com/joeylipton/btctl/internal/state"
)

// Exit codes used by btctl.
const (
	exitSuccess = 0
	exitError   = 1
	exitUsage   = 2
)

// errBadUsage signals "print the short usage banner instead of an error
// message". It is recognised in runMain and never returned by a handler that
// produced its own descriptive message.
var errBadUsage = errors.New("bad usage")

// macAddressRegex matches a colon-separated 48-bit MAC address.
var macAddressRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// hciNameRegex matches a BlueZ hci adapter name (e.g. hci0).
var hciNameRegex = regexp.MustCompile(`^hci[0-9]+$`)

const helpText = `btctl - a minimal Bluetooth manager for Linux

Usage:
  btctl [-v] [-a <hciN>] <command> [options]

Global options:
  -v, --verbose          Log every D-Bus call to stderr
  -a, --adapter <hciN>   Use this adapter for this run (overrides adapter select)
  -h, --help             Show this help page

Commands:
  list                  List all known paired/trusted devices
  connect    <address>  Connect to a device by MAC address
  disconnect <address>  Disconnect from a device by MAC address
  trust      <address>  Mark a device as trusted (auto-reconnects after reboot)
  untrust    <address>  Remove trust from a device
  forget [<address>]    Remove a paired device. Run without arguments for usage.
  pick [-r|--rofi] [-d|--dmenu] [-c|--config <path>]
            Launch the interactive Bluetooth menu (recommended entrypoint).
            Provides full access to all btctl features through a rofi or
            dmenu interface. No CLI arguments needed for normal use.
            -r, --rofi              Force rofi backend
            -d, --dmenu             Force dmenu backend
            -c, --config <path>     Config file for the picker backend
                                    rofi: a .rasi theme/config file
                                    dmenu: a line-separated flags file
  scan [-t <seconds>]   Scan for new devices (default: 10s)
  status                Print current connection status (for polybar/i3bar)
  adapter list          List all Bluetooth adapters
  adapter status        Show status of the active adapter
  adapter power <on|off>       Power the active adapter on or off
  adapter discoverable <on|off>  Enable or disable discoverable mode
  adapter select <hciN>  Set the active adapter and remember it for later runs
  help                  Show this help page

Behaviour:
  btctl automatically marks devices as trusted when connecting. Trusted devices
  reconnect without prompts. Use btctl untrust to remove trust without forgetting
  the device.
  Every command uses the active adapter: the one given with -a, otherwise the
  one saved by "btctl adapter select", otherwise the first hci* adapter.

Options for scan:
  -t, --scan-time <seconds>   How long to scan (must be a positive integer)

Examples:
  btctl list
  btctl connect AA:BB:CC:DD:EE:FF
  btctl trust AA:BB:CC:DD:EE:FF
  btctl scan -t 20
  btctl status
  btctl adapter list
  btctl adapter select hci0

Trust and auto-reconnect:
  Connect and pair automatically set Trusted so devices can reconnect without
  prompts. To remove trust from a device without forgetting it:
    btctl untrust AA:BB:CC:DD:EE:FF

  To check trust status:
    btctl list       (Trusted column visible with -v flag)

  Trust is a permanent setting stored by BlueZ and persists across reboots.
  Forgetting a device (btctl forget) removes both the pairing and trust.

Exit codes:
  0   Success
  1   Runtime error
  2   Usage error

Discoverable mode:
  When discoverable mode is enabled, your Bluetooth adapter is visible
  to nearby devices scanning for Bluetooth hosts. This is required if
  another device needs to initiate pairing with your machine.

  Discoverable mode is disabled by default and has no timeout when enabled
  via btctl (it remains on until you disable it).

  Enable:   btctl adapter discoverable on
  Disable:  btctl adapter discoverable off
  Status:   btctl adapter status

  Discoverable mode can also be toggled from the interactive menu under
  Adapter settings. It is not the same as scanning: scanning finds devices
  near you; discoverable makes your adapter visible to devices scanning
  for you.
`

const usageText = `Usage: btctl [-v] [-a <hciN>] <command> [options]

Run 'btctl help' for the full command list.
`

func main() {
	os.Exit(runMain(os.Args[1:]))
}

// runMain is the only function allowed to translate handler results into a
// process exit code. It returns the exit code; the caller (main) wraps it in
// os.Exit so we satisfy "main() is the only place os.Exit is called".
func runMain(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(helpText)
			return exitSuccess
		}
	}

	opts, args, err := parseGlobalFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		fmt.Fprint(os.Stderr, usageText)
		return exitUsage
	}
	// status feeds status bars, which must never see stderr noise.
	if len(args) > 0 && args[0] == "status" {
		opts.verbose = false
	}
	bluetooth.SetVerbose(opts.verbose)

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return exitUsage
	}

	if args[0] == "help" {
		fmt.Print(helpText)
		return exitSuccess
	}

	code, err := dispatch(args, opts)
	switch {
	case err == nil:
	case errors.Is(err, errBadUsage):
		fmt.Fprint(os.Stderr, usageText)
	default:
		fmt.Fprintf(os.Stderr, "error: %s\n", bluetooth.FormatUserError(err))
		if opts.verbose {
			fmt.Fprintf(os.Stderr, "[v] detail: %v\n", err)
		}
	}
	return code
}

// globalOptions are the flags accepted before the subcommand.
type globalOptions struct {
	verbose bool
	adapter string // from -a/--adapter; "" when not given
}

// parseGlobalFlags consumes leading global flags and returns the rest.
func parseGlobalFlags(args []string) (globalOptions, []string, error) {
	var opts globalOptions
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-v" || a == "--verbose":
			opts.verbose = true
			args = args[1:]
		case a == "-a" || a == "--adapter":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("%s requires an adapter name (e.g. hci0)", a)
			}
			opts.adapter = args[1]
			args = args[2:]
		case strings.HasPrefix(a, "--adapter="):
			opts.adapter = strings.TrimPrefix(a, "--adapter=")
			args = args[1:]
		default:
			if opts.adapter != "" && !hciNameRegex.MatchString(opts.adapter) {
				return opts, nil, fmt.Errorf("invalid adapter name: %s", opts.adapter)
			}
			return opts, args, nil
		}
	}
	if opts.adapter != "" && !hciNameRegex.MatchString(opts.adapter) {
		return opts, nil, fmt.Errorf("invalid adapter name: %s", opts.adapter)
	}
	return opts, args, nil
}

// clientOptions builds the bluetooth.Options for this run. An adapter given
// with -a must exist; the adapter remembered by "adapter select" is only a
// preference, because it may be an unplugged USB dongle.
func clientOptions(opts globalOptions) bluetooth.Options {
	if opts.adapter != "" {
		return bluetooth.Options{Verbose: opts.verbose, Adapter: opts.adapter}
	}
	saved, err := state.LoadAdapter()
	if err != nil && opts.verbose {
		fmt.Fprintf(os.Stderr, "[v] ignoring saved adapter: %v\n", err)
	}
	return bluetooth.Options{Verbose: opts.verbose, Adapter: saved, AdapterOptional: true}
}

// parsedCommand holds validated arguments for a subcommand handler.
type parsedCommand struct {
	name        string
	address     string
	scanSeconds int
	pickCfg     bluetooth.PickerConfig
	adapterArgs []string
}

// dispatch validates arguments for the chosen subcommand and invokes its handler.
func dispatch(args []string, opts globalOptions) (int, error) {
	cmd := args[0]

	if cmd == "status" {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: btctl status takes no arguments")
			return exitUsage, nil
		}
		return runStatus(clientOptions(opts))
	}

	if cmd == "forget" && len(args) == 1 {
		fmt.Println("No address given. Use `btctl list` to see known devices.")
		fmt.Println("Usage: btctl forget <address>")
		return exitSuccess, nil
	}

	parsed, code, err := parseCommandFlags(args)
	if err != nil {
		return code, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := bluetooth.New(ctx, clientOptions(opts))
	if err != nil {
		return exitError, fmt.Errorf("init bluetooth client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// The menu answers pairing requests with pickers; every other command
	// has nobody to ask, so it accepts them.
	var menu *bluetooth.Menu
	var handler bluetooth.PairingHandler = bluetooth.CLIHandler{Interactive: isTerminal(os.Stdin)}
	if parsed.name == "pick" {
		menu = bluetooth.NewMenu(client, parsed.pickCfg)
		handler = menu.PairingHandler()
	}
	unregister, err := registerAgent(ctx, client, handler)
	if err != nil {
		return exitError, err
	}
	defer unregister()

	return executeCommand(ctx, client, menu, opts.verbose, parsed)
}

// isTerminal reports whether f is a character device (a terminal).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// registerAgent registers a pairing agent backed by handler and returns a
// function that unregisters it.
func registerAgent(ctx context.Context, client *bluetooth.Client, handler bluetooth.PairingHandler) (func(), error) {
	agent := bluetooth.NewAgent(handler)
	if err := client.RegisterAgent(ctx, agent); err != nil {
		return nil, fmt.Errorf("register agent: %w", err)
	}
	return func() {
		uctx, ucancel := context.WithTimeout(context.Background(), bluetooth.AgentUnregisterTimeout())
		defer ucancel()
		_ = client.UnregisterAgent(uctx, agent)
	}, nil
}

// executeCommand dispatches to the handler for a validated parsedCommand.
func executeCommand(ctx context.Context, client *bluetooth.Client, menu *bluetooth.Menu, verbose bool, parsed parsedCommand) (int, error) {
	switch parsed.name {
	case "list":
		return runList(ctx, client, verbose)
	case "scan":
		return runScan(ctx, client, parsed.scanSeconds)
	case "connect":
		return runConnect(ctx, client, parsed.address)
	case "disconnect":
		return runDisconnect(ctx, client, parsed.address)
	case "forget":
		return runForget(ctx, client, parsed.address)
	case "trust":
		return runTrust(ctx, client, parsed.address, true)
	case "untrust":
		return runTrust(ctx, client, parsed.address, false)
	case "adapter":
		return runAdapter(ctx, client, parsed.adapterArgs)
	case "pick":
		return runPick(menu)
	default:
		return exitUsage, errBadUsage
	}
}
