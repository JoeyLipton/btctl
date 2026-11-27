// Package desktop shows btctl's feedback outside the terminal: desktop
// notifications (notify-send) and scan progress (notify-send, a zenity
// dialog, or stderr).
//
// notify-send's location and environment are resolved once (sync.Once).
// Sends never fail the caller: notify-send is started before Notify returns
// (so a CLI that exits right away still delivers it) and reaped in the
// background. A missing notify-send is silent; runtime failures are logged
// only with -v.
package desktop

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/joeylipton/btctl/internal/execpath"
)

// Names used in notifications and for the helper programs.
const (
	appName                 = "btctl"
	iconBluetooth           = "bluetooth"
	notifySendExecutable    = "notify-send"
	zenityExecutable        = "zenity"
	notifyTimeoutPersistent = 0    // ms; stays until replaced
	notifyTimeoutComplete   = 2000 // ms
	stderrPrefix            = "[btctl] "
)

// Notification display times in milliseconds.
const (
	// notifyDurationStatus is the display time for connection status
	// notifications. Long enough to read, short enough not to linger.
	notifyDurationStatus = 4000

	// notifyDurationAlert is for pairing requests, which need more attention
	// time so the user can react before the agent times out.
	notifyDurationAlert = 8000
)

// notifyResolver caches notify-send's location and environment. It is
// resolved once on first use and then read without locking.
type notifyResolver struct {
	once      sync.Once
	available bool
	path      string
	env       []string
}

// notifyState is the process-wide notifier. Tests replace it.
var notifyState = &notifyResolver{}

// disabledNotifier returns a resolver that never sends anything.
func disabledNotifier() *notifyResolver {
	r := &notifyResolver{}
	r.once.Do(func() {})
	return r
}

// verbose controls whether notify-send failures are logged to stderr. It is
// atomic because background goroutines read it.
var verbose atomic.Bool

// SetVerbose controls whether notification failures are logged to stderr.
func SetVerbose(v bool) { verbose.Store(v) }

// resolveNotifyState performs the one-time notify-send PATH lookup. It must be
// the only place that resolves availability so the sync.Once is never consumed
// by a no-op (which would permanently disable notifications).
func resolveNotifyState() *notifyResolver {
	r := notifyState
	r.once.Do(func() {
		// execpath.Find copes with the stripped PATH of i3/polybar launches.
		// A missing notify-send is not an error.
		if p, err := execpath.Find(notifySendExecutable); err == nil {
			r.available = true
			r.path = p
			r.env = notifyEnv()
		}
	})
	return r
}

// Notify shows a status notification from btctl without waiting for it.
// It is a no-op when notify-send is not installed.
func Notify(body string) {
	notifyWithDuration(appName, body, notifyDurationStatus)
}

// Alert shows a longer-lasting notification with its own title, for things
// the user has to act on (pairing requests and codes).
func Alert(title, body string) {
	notifyWithDuration(title, body, notifyDurationAlert)
}

func notifyWithDuration(title, body string, durationMs int) {
	r := resolveNotifyState()
	if !r.available {
		return
	}
	// Flags precede the summary/body positionals; some notify-send builds are
	// strict about argument order.
	args := []string{
		"-a", appName,
		"-t", fmt.Sprintf("%d", durationMs),
		"-i", iconBluetooth,
		title,
		body,
	}
	cmd := exec.Command(r.path, args...)
	// notify-send reaches the notification daemon over the session bus;
	// an i3 keybind launch may lack DBUS_SESSION_BUS_ADDRESS, so supply it.
	cmd.Env = r.env
	// Start synchronously: the CLI often exits right after notifying, and a
	// started notify-send finishes on its own after btctl is gone. Only the
	// wait (reaping the child) happens in the background.
	if err := cmd.Start(); err != nil {
		if verbose.Load() {
			fmt.Fprintf(os.Stderr, "%snotify-send failed: %v\n", stderrPrefix, err)
		}
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil && verbose.Load() {
			fmt.Fprintf(os.Stderr, "%snotify-send failed: %v\n", stderrPrefix, err)
		}
	}()
}

// notifyEnv returns an environment slice for notify-send that ensures
// DBUS_SESSION_BUS_ADDRESS is present. When btctl is launched from i3 without
// a full environment, notify-send cannot reach the notification daemon without
// it. DISPLAY (if set) is inherited via os.Environ.
func notifyEnv() []string {
	env := os.Environ()

	// Already set: nothing to do.
	for _, e := range env {
		if strings.HasPrefix(e, "DBUS_SESSION_BUS_ADDRESS=") {
			return env
		}
	}

	// Strategy 1: ask the systemd user manager (most reliable on systemd
	// distros, including Arch) for the value it holds for this session.
	if addr := resolveDBusAddressFromSystemd(); addr != "" {
		return append(env, "DBUS_SESSION_BUS_ADDRESS="+addr)
	}

	// Strategy 2: the standard per-user bus socket path.
	if addr := resolveDBusAddressFromSocket(); addr != "" {
		return append(env, "DBUS_SESSION_BUS_ADDRESS="+addr)
	}

	// Could not resolve; return env unchanged. notify-send may still
	// auto-discover /run/user/$UID/bus on some setups.
	return env
}

// resolveDBusAddressFromSystemd reads DBUS_SESSION_BUS_ADDRESS from the
// systemd user manager environment, or "" if unavailable.
func resolveDBusAddressFromSystemd() string {
	path, err := execpath.Find("systemctl")
	if err != nil {
		return ""
	}
	out, err := exec.Command(path, "--user", "show-environment").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "DBUS_SESSION_BUS_ADDRESS=") {
			return strings.TrimPrefix(line, "DBUS_SESSION_BUS_ADDRESS=")
		}
	}
	return ""
}

// resolveDBusAddressFromSocket returns the standard systemd per-user bus
// socket address when the socket exists, or "" otherwise.
func resolveDBusAddressFromSocket() string {
	candidate := fmt.Sprintf("/run/user/%d/bus", os.Getuid())
	if _, err := os.Stat(candidate); err == nil {
		return "unix:path=" + candidate
	}
	return ""
}

// notifySendSupportsPrintID reports whether the installed notify-send supports
// --print-id, used by scan progress to replace a notification in place rather
// than stacking. --help goes to stderr on some distros (libnotify/Ubuntu) and
// stdout on others, so both streams are captured.
func notifySendSupportsPrintID() bool {
	r := resolveNotifyState()
	if !r.available {
		return false
	}
	out, _ := exec.Command(r.path, "--help").CombinedOutput()
	return bytes.Contains(out, []byte("--print-id"))
}
