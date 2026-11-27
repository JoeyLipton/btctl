package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/menu"
)

// pickUsageLine is reused by every parsePickArgs error path so the usage hint
// stays in sync with the help page.
const pickUsageLine = "usage: btctl pick [-r|--rofi] [-d|--dmenu] [-c|--config <path>]"

// scanNameWidth is the fixed column width used when rendering scan-result
// names in the post-scan numbered list.
const scanNameWidth = 20

// parseCommandFlags validates subcommand arguments after the command name.
func parseCommandFlags(args []string) (parsedCommand, int, error) {
	cmd := args[0]
	var parsed parsedCommand
	parsed.name = cmd

	switch cmd {
	case "list":
		if len(args) != 1 {
			return parsed, exitUsage, errBadUsage
		}
	case "pick":
		cfg, code, err := parsePickFlags(args[1:])
		if err != nil {
			return parsed, code, err
		}
		parsed.pickCfg = cfg
	case "scan":
		seconds, err := parseScanArgs(args[1:])
		if err != nil {
			return parsed, exitUsage, err
		}
		parsed.scanSeconds = seconds
	case "adapter":
		sub, target, code, err := parseAdapterSubcommand(args[1:])
		if err != nil {
			return parsed, code, err
		}
		parsed.adapterArgs = []string{sub}
		if target != "" {
			parsed.adapterArgs = append(parsed.adapterArgs, target)
		}
	case "connect", "pair", "disconnect", "forget", "trust", "untrust":
		if len(args) != 2 {
			return parsed, exitUsage, errBadUsage
		}
		parsed.address = args[1]
		if !bluez.IsAddress(parsed.address) {
			return parsed, exitUsage, fmt.Errorf("invalid MAC address: %s", parsed.address)
		}
	default:
		return parsed, exitUsage, errBadUsage
	}
	return parsed, exitSuccess, nil
}

// parsePickFlags parses flags for the pick subcommand.
func parsePickFlags(args []string) (menu.Config, int, error) {
	var sawRofi, sawDmenu bool
	var configFile string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-r", "--rofi":
			sawRofi = true
		case "-d", "--dmenu":
			sawDmenu = true
		case "-c", "--config":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "flag requires an argument: %s\n", a)
				fmt.Fprintln(os.Stderr, pickUsageLine)
				return menu.Config{}, exitUsage, errBadUsage
			}
			configFile = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown flag for pick: %s\n", a)
			fmt.Fprintln(os.Stderr, pickUsageLine)
			return menu.Config{}, exitUsage, errBadUsage
		}
	}
	if sawRofi && sawDmenu {
		fmt.Fprintln(os.Stderr, "cannot use --rofi and --dmenu together")
		return menu.Config{}, exitUsage, errBadUsage
	}

	cfg := menu.Config{ConfigFile: configFile}
	switch {
	case sawRofi:
		cfg.Backend = menu.BackendRofi
	case sawDmenu:
		cfg.Backend = menu.BackendDmenu
	default:
		cfg.Backend = menu.BackendAuto
	}
	return cfg, exitSuccess, nil
}

// parseScanArgs parses the flag set accepted by `btctl scan`.
func parseScanArgs(args []string) (int, error) {
	seconds := int(bluez.DefaultScanDuration.Seconds())

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t", "--scan-time":
			if i+1 >= len(args) {
				return 0, fmt.Errorf("scan: missing value for %s", args[i])
			}
			raw := args[i+1]
			n, err := strconv.Atoi(raw)
			if err != nil {
				return 0, fmt.Errorf("scan: invalid value %q for %s (expected integer seconds)", raw, args[i])
			}
			if n <= 0 {
				return 0, fmt.Errorf("scan: %s must be a positive integer, got %d", args[i], n)
			}
			d := time.Duration(n) * time.Second
			if d < bluez.MinScanDuration || d > bluez.MaxScanDuration {
				return 0, fmt.Errorf("scan: duration must be between %s and %s, got %d seconds",
					bluez.MinScanDuration, bluez.MaxScanDuration, n)
			}
			seconds = n
			i++
		default:
			return 0, fmt.Errorf("scan: unrecognized flag %q", args[i])
		}
	}
	return seconds, nil
}

// parseAdapterSubcommand validates and parses the adapter subcommand tree.
func parseAdapterSubcommand(args []string) (subcommand, target string, code int, err error) {
	if len(args) == 0 {
		return "", "", exitUsage, errBadUsage
	}
	sub := args[0]
	switch sub {
	case "list", "status":
		if len(args) != 1 {
			return "", "", exitUsage, errBadUsage
		}
		return sub, "", exitSuccess, nil
	case "power", "discoverable":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return "", "", exitUsage, errBadUsage
		}
		return sub, args[1], exitSuccess, nil
	case "select":
		if len(args) != 2 {
			return "", "", exitUsage, errBadUsage
		}
		if !bluez.IsAdapterName(args[1]) {
			return "", "", exitUsage, fmt.Errorf("invalid adapter name: %s", args[1])
		}
		return sub, args[1], exitSuccess, nil
	default:
		return "", "", exitUsage, errBadUsage
	}
}
