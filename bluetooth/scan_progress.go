// Package bluetooth - scan_progress.go shows scan progress to the user outside
// the device list itself. It picks one of three back ends at runtime: a desktop
// notification (notify-send), a zenity pulsing dialog, or plain stderr lines.
// It does not affect the scan result returned to the caller; it is purely
// informational feedback while discovery runs.
package bluetooth

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/joeylipton/btctl/internal/textfmt"
)

// scanProgress displays scan progress outside the picker process. Implementations
// are selected at runtime by newScanProgress.
type scanProgress interface {
	Start(durationSecs int)
	DeviceFound(name, address string)
	Complete(count int)
}

// notificationDaemonAvailable reports whether a notification daemon is likely
// reachable. It checks for an explicit DBUS_SESSION_BUS_ADDRESS or the standard
// systemd per-user bus socket; it does not send a test notification. This is a
// better gate than a TTY check: a terminal-launched rofi still wants desktop
// notifications, and a daemon-less environment must always fall back to stderr.
func notificationDaemonAvailable() bool {
	if addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" {
		return true
	}
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid()))
	return err == nil
}

// newScanProgress selects notify-send, zenity, or stderr progress display.
// It is called once per scan so the mechanism is re-evaluated each time.
//
// When no notification infrastructure is available, stderr is used regardless
// of launch context so the user always gets feedback. Otherwise notify-send is
// preferred (non-intrusive, works from any launch context), then zenity.
func newScanProgress(verbose bool) scanProgress {
	stderr := &stderrProgress{w: os.Stderr}
	if !notificationDaemonAvailable() {
		return stderr
	}
	// notify-send comes from the shared notifier (which resolves it despite
	// stripped i3/polybar PATHs and caches its environment); zenity goes
	// through resolveBinary for the same reason.
	if r := resolveNotifyState(); r.available {
		return &notifySendProgress{
			verbose:    verbose,
			usePrintID: notifySendSupportsPrintID(),
			stderr:     stderr,
			path:       r.path,
			env:        r.env,
		}
	}
	if path, err := resolveBinary(zenityExecutable); err == nil {
		return &zenityProgress{verbose: verbose, stderr: stderr, zenityPath: path}
	}
	return stderr
}

// notifySendProgress uses desktop notifications for scan progress.
type notifySendProgress struct {
	verbose    bool
	usePrintID bool
	id         uint32
	foundCount int
	degraded   bool
	stderr     *stderrProgress
	path       string   // resolved absolute path to notify-send
	env        []string // environment for notify-send (see notifyEnv)
}

func (n *notifySendProgress) degrade(err error) {
	if n.verbose {
		fmt.Fprintf(os.Stderr, "%sprogress display unavailable, falling back to stderr: %v\n", stderrPrefix, err)
	}
	n.degraded = true
}

// Start opens or updates the notify-send progress notification for the scan window.
func (n *notifySendProgress) Start(durationSecs int) {
	if n.degraded {
		n.stderr.Start(durationSecs)
		return
	}
	body := fmt.Sprintf("Scanning for %d seconds...", durationSecs)
	if err := n.send(body, notifyTimeoutPersistent, true); err != nil {
		n.degrade(err)
		n.stderr.Start(durationSecs)
	}
}

// DeviceFound updates the notification with the running count of discovered devices.
func (n *notifySendProgress) DeviceFound(name, address string) {
	if n.degraded {
		n.stderr.DeviceFound(name, address)
		return
	}
	n.foundCount++
	body := fmt.Sprintf("Scanning... %d found so far", n.foundCount)
	if err := n.send(body, notifyTimeoutPersistent, false); err != nil {
		n.degrade(err)
	}
}

// Complete replaces the notification with a scan-complete summary.
func (n *notifySendProgress) Complete(count int) {
	if n.degraded {
		n.stderr.Complete(count)
		return
	}
	body := fmt.Sprintf("Scan complete. %d device(s) found.", count)
	_ = n.send(body, notifyTimeoutComplete, false)
}

func (n *notifySendProgress) send(body string, timeoutMs int, captureID bool) error {
	// -a sets the application name so notification centres and Do-Not-Disturb
	// rules can group/filter btctl's notifications. The first positional is the
	// summary (title), the second is the body.
	args := []string{
		"-a", notifyAppName,
		"-t", strconv.Itoa(timeoutMs),
		"-i", notifyIconBluetooth,
		notifyAppName, body,
	}
	if n.id != 0 {
		args = append([]string{"--replace-id", strconv.FormatUint(uint64(n.id), 10)}, args...)
	} else if captureID && n.usePrintID {
		args = append([]string{"--print-id"}, args...)
	}
	cmd := exec.Command(n.path, args...)
	cmd.Env = n.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("notify-send: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if captureID && n.usePrintID {
		idStr := strings.TrimSpace(string(out))
		if id, parseErr := strconv.ParseUint(idStr, 10, 32); parseErr == nil {
			n.id = uint32(id)
		}
	}
	return nil
}

// stderrProgress prints scan progress to an injectable writer (os.Stderr in
// production). The nil guard defaults w to os.Stderr so a zero-value value is
// still usable.
type stderrProgress struct {
	w io.Writer
}

// Start prints the scan duration line to the configured writer.
func (s *stderrProgress) Start(durationSecs int) {
	if s.w == nil {
		s.w = os.Stderr
	}
	fmt.Fprintf(s.w, "%sscanning for %d seconds...\n", stderrPrefix, durationSecs)
}

// DeviceFound prints one discovered device line to the configured writer.
func (s *stderrProgress) DeviceFound(name, address string) {
	if s.w == nil {
		s.w = os.Stderr
	}
	name = textfmt.Truncate(name, deviceNameWidth)
	fmt.Fprintf(s.w, "%sfound:   %-*s %s\n", stderrPrefix, deviceNameWidth, name, address)
}

// Complete prints the scan-complete summary to the configured writer.
func (s *stderrProgress) Complete(count int) {
	if s.w == nil {
		s.w = os.Stderr
	}
	fmt.Fprintf(s.w, "%sscan complete. %d device(s) found.\n", stderrPrefix, count)
}

// zenityProgress uses a zenity --progress dialog for scan feedback.
type zenityProgress struct {
	verbose    bool
	degraded   bool
	stderr     *stderrProgress
	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	zenityPath string // resolved absolute path to zenity
}

func (z *zenityProgress) degrade(err error) {
	if z.verbose {
		fmt.Fprintf(os.Stderr, "%sprogress display unavailable, falling back to stderr: %v\n", stderrPrefix, err)
	}
	z.degraded = true
}

// Start opens the zenity pulsing progress dialog for the scan window.
func (z *zenityProgress) Start(durationSecs int) {
	if z.degraded {
		z.stderr.Start(durationSecs)
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	cmd := exec.Command(z.zenityPath,
		"--progress",
		"--title=btctl",
		fmt.Sprintf("--text=Scanning for %d seconds...", durationSecs),
		"--pulsate",
		"--auto-close",
		"--no-cancel",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		z.degrade(err)
		z.stderr.Start(durationSecs)
		return
	}
	if err := cmd.Start(); err != nil {
		z.degrade(err)
		z.stderr.Start(durationSecs)
		return
	}
	z.cmd = cmd
	z.stdin = stdin
}

// DeviceFound appends a device line to the zenity progress dialog.
func (z *zenityProgress) DeviceFound(name, address string) {
	if z.degraded {
		z.stderr.DeviceFound(name, address)
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.stdin == nil {
		return
	}
	if _, err := fmt.Fprintf(z.stdin, "# %s  %s\n", name, address); err != nil {
		z.degrade(err)
		z.stderr.DeviceFound(name, address)
	}
}

// Complete closes the zenity dialog and prints the final count if degraded.
func (z *zenityProgress) Complete(count int) {
	if z.degraded {
		z.stderr.Complete(count)
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.stdin != nil {
		_, _ = fmt.Fprintf(z.stdin, "# Scan complete. %d device(s) found.\n", count)
		_ = z.stdin.Close()
		z.stdin = nil
	}
	if z.cmd != nil && z.cmd.Process != nil {
		_ = z.cmd.Process.Kill()
		_ = z.cmd.Wait()
		z.cmd = nil
	}
}
