// Package bluetooth - picker.go is the interactive `btctl pick` menu. It drives
// rofi or dmenu as a thin selection front end and keeps exactly one picker
// process open at a time; all result/progress reporting goes to stderr. It owns
// the connect-with-confirmation flow that relays BlueZ agent prompts to a
// yes/no picker. It contains no Bluetooth protocol logic of its own beyond
// calling the Client.
package bluetooth

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// deviceAddressPattern matches a MAC address embedded in a picker line.
var deviceAddressPattern = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// PickerBackend selects which interactive picker the Menu will spawn.
type PickerBackend int

const (
	// BackendAuto prefers rofi when it is on PATH and falls back to dmenu;
	// errors only when neither is available.
	BackendAuto PickerBackend = iota
	// BackendRofi forces rofi and refuses to fall back.
	BackendRofi
	// BackendDmenu forces dmenu and refuses to fall back.
	BackendDmenu
)

// PickerConfig bundles every option the picker needs to invoke a backend.
type PickerConfig struct {
	Backend    PickerBackend
	ConfigFile string // empty string means no config file
}

// Menu is the self-contained interactive driver behind `btctl pick`. Pairing
// prompts reach it through PairingHandler: register an Agent built from that
// handler while the menu runs so BlueZ requests become yes/no pickers.
type Menu struct {
	client *Client
	cfg    PickerConfig
}

// NewMenu returns a Menu bound to the given client and picker configuration.
// The caller retains ownership of the client.
func NewMenu(client *Client, cfg PickerConfig) *Menu {
	return &Menu{client: client, cfg: cfg}
}

// mainMenuEntry describes one top-level menu item. fn==nil marks Quit.
type mainMenuEntry struct {
	label string
	fn    func(*Menu) error
}

// mainMenuEntries is the fixed top-level menu. Entry order is also display
// order; the labels are matched verbatim against the picker's stdout to map
// selections back to handlers.
var mainMenuEntries = []mainMenuEntry{
	{"[+] Connect a device", (*Menu).menuConnect},
	{"[-] Disconnect a device", (*Menu).menuDisconnect},
	{"[x] Forget a device", (*Menu).menuForget},
	{"[?] Scan for new devices", (*Menu).menuScan},
	{"[i] Device info", (*Menu).menuInfo},
	{"[a] Adapter settings", (*Menu).menuAdapter},
	{"[q] Quit", nil},
}

// Run drives the top-level navigation loop. It shows the main menu,
// dispatches to a sub-menu based on selection, then returns to the main
// menu unless the user explicitly chooses Quit or dismisses the top-level
// menu (Escape).
func (m *Menu) Run() error {
	labels := make([]string, len(mainMenuEntries))
	for i, e := range mainMenuEntries {
		labels[i] = e.label
	}
	for {
		choice, err := m.prompt("btctl", labels)
		if err != nil {
			return err
		}
		if choice == "" {
			return nil
		}
		fn, ok := lookupMainMenu(choice)
		if !ok {
			continue
		}
		if fn == nil {
			return nil
		}
		if err := fn(m); err != nil {
			return err
		}
	}
}

// lookupMainMenu returns the handler associated with label, plus ok=true if
// the label was a known main-menu entry.
func lookupMainMenu(label string) (func(*Menu) error, bool) {
	for _, e := range mainMenuEntries {
		if e.label == label {
			return e.fn, true
		}
	}
	return nil, false
}

// prompt invokes the configured picker with the given title and lines and
// returns the line the user selected.
func (m *Menu) prompt(title string, lines []string) (string, error) {
	return m.promptContext(context.Background(), title, lines)
}

// promptContext is prompt bound to ctx: cancelling ctx closes the picker.
func (m *Menu) promptContext(ctx context.Context, title string, lines []string) (string, error) {
	cmd, isDmenu, err := m.pickerCmd(ctx, title)
	if err != nil {
		return "", err
	}

	var stdin bytes.Buffer
	var header string
	if isDmenu {
		header = dmenuTitlePrefix + title
		fmt.Fprintln(&stdin, header)
	}
	for _, line := range lines {
		fmt.Fprintln(&stdin, line)
	}

	cmd.Stdin = &stdin
	// When ctx is cancelled the picker is killed; WaitDelay stops Wait from
	// blocking on any child process that still holds its output pipe.
	cmd.WaitDelay = pickerWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			if code == 1 {
				return "", nil
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				return "", fmt.Errorf("picker exited with code %d", code)
			}
			return "", fmt.Errorf("picker exited with code %d: %s", code, msg)
		}
		return "", fmt.Errorf("running picker: %w", runErr)
	}

	selected := strings.TrimRight(stdout.String(), "\r\n")
	if selected == "" {
		return "", nil
	}
	if isDmenu && selected == header {
		return "", nil
	}
	return selected, nil
}

// pickerCmd builds the picker invocation for the configured backend.
func (m *Menu) pickerCmd(ctx context.Context, title string) (*exec.Cmd, bool, error) {
	backend, err := resolveBackend(m.cfg)
	if err != nil {
		return nil, false, err
	}
	switch backend {
	case rofiExecutable:
		cmd, err := rofiPromptCmd(ctx, title, m.cfg.ConfigFile)
		return cmd, false, err
	default:
		cmd, err := dmenuCmd(ctx, m.cfg.ConfigFile)
		return cmd, true, err
	}
}

// resolveBackend returns the picker executable name based on cfg and
// availability. Binary resolution goes through resolveBinary so a stripped
// PATH (i3/polybar) still finds rofi and dmenu under known absolute prefixes.
func resolveBackend(cfg PickerConfig) (string, error) {
	switch cfg.Backend {
	case BackendRofi:
		if _, err := resolveBinary(rofiExecutable); err != nil {
			return "", fmt.Errorf("resolve picker backend: rofi not found: %w", ErrNoPicker)
		}
		return rofiExecutable, nil
	case BackendDmenu:
		if _, err := resolveBinary(dmenuExecutable); err != nil {
			return "", fmt.Errorf("resolve picker backend: dmenu not found: %w", ErrNoPicker)
		}
		return dmenuExecutable, nil
	default:
		if _, err := resolveBinary(rofiExecutable); err == nil {
			return rofiExecutable, nil
		}
		if _, err := resolveBinary(dmenuExecutable); err == nil {
			return dmenuExecutable, nil
		}
		return "", ErrNoPicker
	}
}

// rofiPromptCmd builds a rofi invocation that uses title as the -p prompt
// and applies the optional config file via "-config <path>".
func rofiPromptCmd(ctx context.Context, title, configFile string) (*exec.Cmd, error) {
	path, err := resolveBinary(rofiExecutable)
	if err != nil {
		return nil, fmt.Errorf("rofi prompt: rofi not found: %w", ErrNoPicker)
	}
	args := []string{"-dmenu", "-i", "-p", title}
	if configFile != "" {
		if err := validatePickerConfigFile("rofi", configFile); err != nil {
			return nil, err
		}
		args = append(args, "-config", configFile)
	}
	return exec.CommandContext(ctx, path, args...), nil
}

// dmenuCmd builds the dmenu invocation. When configFile is non-empty it is
// validated and parsed into extra dmenu arguments.
func dmenuCmd(ctx context.Context, configFile string) (*exec.Cmd, error) {
	path, err := resolveBinary(dmenuExecutable)
	if err != nil {
		return nil, fmt.Errorf("dmenu prompt: dmenu not found: %w", ErrNoPicker)
	}
	var extraArgs []string
	if configFile != "" {
		if err := validatePickerConfigFile("dmenu", configFile); err != nil {
			return nil, err
		}
		flags, err := parseDmenuConfigFile(configFile)
		if err != nil {
			return nil, err
		}
		extraArgs = flags
	}
	return exec.CommandContext(ctx, path, extraArgs...), nil
}

// validatePickerConfigFile reports a uniform "not found" error if path is
// missing or refers to a directory.
func validatePickerConfigFile(backend, path string) error {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fmt.Errorf("%s config file not found: %s", backend, path)
	}
	return nil
}

// parseDmenuConfigFile reads a dmenu config file and returns its non-comment,
// non-blank lines as a slice of arguments suitable for appending to a dmenu
// invocation.
func parseDmenuConfigFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dmenu config file %s: %w", path, err)
	}
	defer f.Close()

	var args []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		args = append(args, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dmenu config file %s: %w", path, err)
	}
	return args, nil
}

// menuStderr writes one operator-facing line to stderr with the shared
// [btctl] prefix.
func menuStderr(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, stderrPrefix+format+"\n", args...)
}

// formatDeviceLines turns a slice of devices into the per-line strings fed
// to the picker.
func formatDeviceLines(devs []Device) []string {
	lines := make([]string, len(devs))
	for i, d := range devs {
		lines[i] = buildDeviceLine(d, 0, false)
	}
	return lines
}

// matchDeviceByAddress maps a picker output line back to one of the devices.
func matchDeviceByAddress(devices []Device, line string) *Device {
	mac, ok := extractAddressFromLine(line)
	if !ok {
		return nil
	}
	for i := range devices {
		if strings.ToLower(devices[i].Address) == mac {
			return &devices[i]
		}
	}
	return nil
}

// extractAddressFromLine parses the MAC address from the last valid field
// in a picker output line.
func extractAddressFromLine(line string) (string, bool) {
	for _, f := range strings.Fields(line) {
		if deviceAddressPattern.MatchString(f) {
			return strings.ToLower(f), true
		}
	}
	return "", false
}

// devicePresentName returns a printable name for the device, substituting
// "(unknown)" when BlueZ has not yet supplied a real one.
func devicePresentName(d Device) string {
	if d.Name == "" || d.Name == "Unknown" {
		return "(unknown)"
	}
	return d.Name
}

// yesNo renders a bool as the ASCII strings used in the Device info menu.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// buildDeviceLine formats a device as a fixed-width picker line. When
// showIndex is true, index is rendered as a numbered prefix for scan results.
func buildDeviceLine(d Device, index int, showIndex bool) string {
	status := ""
	switch {
	case d.Connected:
		status = "connected"
	case d.Paired:
		status = "paired"
	}
	name := devicePresentName(d)
	if len(name) > deviceNameWidth {
		name = name[:deviceNameWidth]
	}
	var line string
	if showIndex {
		line = fmt.Sprintf("%*d) %-*s %s", scanIndexWidth, index, deviceNameWidth, name, d.Address)
	} else {
		line = fmt.Sprintf("[%-*s] %-*s %s", statusTagInnerWidth, status, deviceNameWidth, name, d.Address)
	}
	if d.Battery > batteryUnavailable {
		line += fmt.Sprintf("  %d%%", d.Battery)
	}
	return line
}
