// Package menu is the interactive `btctl pick` menu. It drives rofi or dmenu
// as a thin selection front end and keeps one picker process open at a time.
// Pairing prompts from BlueZ reach it through PairingHandler (pairing.go). It
// contains no Bluetooth logic of its own beyond calling the bluez client.
//
// The menu is usually opened from a keybinding or a status bar, where nobody
// reads stderr, so outcomes the user must see are also raised as desktop
// notifications (see tell).
package menu

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/desktop"
	"github.com/joeylipton/btctl/internal/execpath"
	"github.com/joeylipton/btctl/internal/textfmt"
)

// ErrNoPicker is returned when neither rofi nor dmenu is installed.
var ErrNoPicker = errors.New("no picker found: install rofi or dmenu")

// Backend selects which picker the menu runs.
type Backend int

const (
	// BackendAuto prefers rofi and falls back to dmenu.
	BackendAuto Backend = iota
	// BackendRofi forces rofi.
	BackendRofi
	// BackendDmenu forces dmenu.
	BackendDmenu
)

// Config bundles the picker options from the command line.
type Config struct {
	Backend    Backend
	ConfigFile string // rofi: a .rasi file; dmenu: a file of extra arguments, one per line
}

const (
	rofiExecutable  = "rofi"
	dmenuExecutable = "dmenu"

	// dmenuTitlePrefix marks the synthetic title line dmenu pickers start with
	// (dmenu has no prompt title of its own in every build).
	dmenuTitlePrefix = "> "

	// pickerWaitDelay is how long a killed picker may keep its pipes open.
	pickerWaitDelay = time.Second

	// nameLookupTimeout bounds the D-Bus call that fetches a device name for
	// a pairing notification.
	nameLookupTimeout = 3 * time.Second

	// Column widths of picker lines.
	deviceNameWidth   = 24
	statusInnerWidth  = 9 // fits "connected"
	scanIndexWidth    = 3
	adapterNameWidth  = 6
	adapterAddrWidth  = 17
	infoLabelWidth    = 12
	adapterLabelWidth = 14

	stderrPrefix = "[btctl] "
)

// Menu is the interactive driver behind `btctl pick`.
type Menu struct {
	client *bluez.Client
	cfg    Config

	stderr        io.Writer
	stderrVisible bool                     // stderr is a terminal someone reads
	notify        func(body string)        // desktop.Notify; replaced in tests
	alert         func(title, body string) // desktop.Alert; replaced in tests
}

// New returns a Menu bound to client. The caller keeps ownership of client
// and must register an agent built from PairingHandler while the menu runs.
func New(client *bluez.Client, cfg Config) *Menu {
	return &Menu{
		client:        client,
		cfg:           cfg,
		stderr:        os.Stderr,
		stderrVisible: isTerminal(os.Stderr),
		notify:        desktop.Notify,
		alert:         desktop.Alert,
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// mainMenuEntries is the fixed top-level menu, in display order.
var mainMenuEntries = []struct {
	label string
	fn    func(*Menu, context.Context) error // nil means Quit
}{
	{"[+] Connect a device", (*Menu).menuConnect},
	{"[-] Disconnect a device", (*Menu).menuDisconnect},
	{"[x] Forget a device", (*Menu).menuForget},
	{"[?] Scan for new devices", (*Menu).menuScan},
	{"[i] Device info", (*Menu).menuInfo},
	{"[a] Adapter settings", (*Menu).menuAdapter},
	{"[q] Quit", nil},
}

// Run shows the main menu until the user quits or dismisses it (Escape) or
// ctx is cancelled. Sub-menus return to the main menu when they finish.
func (m *Menu) Run(ctx context.Context) (err error) {
	defer func() {
		// The caller prints err to stderr; from a keybinding that is invisible.
		if err != nil && !m.stderrVisible {
			m.notify("btctl pick: " + bluez.FormatUserError(err))
		}
	}()
	labels := make([]string, len(mainMenuEntries))
	for i, e := range mainMenuEntries {
		labels[i] = e.label
	}
	for ctx.Err() == nil {
		choice, err := m.prompt(ctx, "btctl", labels)
		if err != nil || choice == "" {
			return err
		}
		for _, e := range mainMenuEntries {
			if e.label != choice {
				continue
			}
			if e.fn == nil {
				return nil
			}
			if err := e.fn(m, ctx); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// prompt shows lines in the picker and returns the line the user chose, or
// "" when they dismissed it. Cancelling ctx closes the picker.
func (m *Menu) prompt(ctx context.Context, title string, lines []string) (string, error) {
	if ctx.Err() != nil {
		return "", nil
	}
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
		if ctx.Err() != nil {
			return "", nil
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			if code == 1 { // Escape
				return "", nil
			}
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", fmt.Errorf("picker exited with code %d: %s", code, msg)
			}
			return "", fmt.Errorf("picker exited with code %d", code)
		}
		return "", fmt.Errorf("running picker: %w", runErr)
	}

	selected := strings.TrimRight(stdout.String(), "\r\n")
	if isDmenu && selected == header {
		return "", nil
	}
	return selected, nil
}

// pickerCmd builds the picker invocation for the configured backend.
func (m *Menu) pickerCmd(ctx context.Context, title string) (cmd *exec.Cmd, isDmenu bool, err error) {
	backend, path, err := resolveBackend(m.cfg.Backend)
	if err != nil {
		return nil, false, err
	}
	if backend == rofiExecutable {
		args := []string{"-dmenu", "-i", "-p", title}
		if m.cfg.ConfigFile != "" {
			if err := checkConfigFile("rofi", m.cfg.ConfigFile); err != nil {
				return nil, false, err
			}
			args = append(args, "-config", m.cfg.ConfigFile)
		}
		return exec.CommandContext(ctx, path, args...), false, nil
	}
	var args []string
	if m.cfg.ConfigFile != "" {
		if err := checkConfigFile("dmenu", m.cfg.ConfigFile); err != nil {
			return nil, false, err
		}
		if args, err = readDmenuArgs(m.cfg.ConfigFile); err != nil {
			return nil, false, err
		}
	}
	return exec.CommandContext(ctx, path, args...), true, nil
}

// resolveBackend returns the picker to run and its path. execpath.Find copes
// with the stripped PATH of i3/polybar launches.
func resolveBackend(backend Backend) (name, path string, err error) {
	var candidates []string
	switch backend {
	case BackendRofi:
		candidates = []string{rofiExecutable}
	case BackendDmenu:
		candidates = []string{dmenuExecutable}
	default:
		candidates = []string{rofiExecutable, dmenuExecutable}
	}
	for _, name := range candidates {
		if path, err := execpath.Find(name); err == nil {
			return name, path, nil
		}
	}
	if len(candidates) == 1 {
		return "", "", fmt.Errorf("%s not found: %w", candidates[0], ErrNoPicker)
	}
	return "", "", ErrNoPicker
}

// checkConfigFile reports a uniform error if path is missing or a directory.
func checkConfigFile(backend, path string) error {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fmt.Errorf("%s config file not found: %s", backend, path)
	}
	return nil
}

// readDmenuArgs reads a dmenu config file: one argument per line, blank lines
// and lines starting with # ignored.
func readDmenuArgs(path string) ([]string, error) {
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

// progressf writes a progress line to stderr.
func (m *Menu) progressf(format string, args ...any) {
	fmt.Fprintf(m.stderr, stderrPrefix+format+"\n", args...)
}

// tell reports an outcome the user has to see: on stderr, and as a desktop
// notification unless stderr is a terminal (where it would be a duplicate).
func (m *Menu) tell(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(m.stderr, "%s%s\n", stderrPrefix, msg)
	if !m.stderrVisible {
		m.notify(msg)
	}
}

// deviceLine formats a device as a fixed-width picker line. With index > 0
// it is a numbered scan result instead of a status-tagged device.
func deviceLine(d bluez.Device, index int) string {
	name := textfmt.Truncate(d.DisplayName(), deviceNameWidth)
	var line string
	if index > 0 {
		line = fmt.Sprintf("%*d) %-*s %s", scanIndexWidth, index, deviceNameWidth, name, d.Address)
	} else {
		line = fmt.Sprintf("[%-*s] %-*s %s", statusInnerWidth, d.Status(), deviceNameWidth, name, d.Address)
	}
	if d.Battery != bluez.BatteryUnavailable {
		line += fmt.Sprintf("  %d%%", d.Battery)
	}
	return line
}

// deviceForLine maps a picker line back to one of devices by the MAC address
// it contains.
func deviceForLine(devices []bluez.Device, line string) (bluez.Device, bool) {
	for _, field := range strings.Fields(line) {
		if !bluez.IsAddress(field) {
			continue
		}
		for _, d := range devices {
			if strings.EqualFold(d.Address, field) {
				return d, true
			}
		}
	}
	return bluez.Device{}, false
}
