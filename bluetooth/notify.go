// Package bluetooth - notify.go is the single entry point for desktop
// notifications via notify-send. Every notify-send invocation in the package
// resolves availability through here exactly once (sync.Once), and all sends
// are fire-and-forget: they run in a goroutine and never block or fail the
// caller's control flow. notify-send absence is silent; runtime failures are
// logged only under -v.
package bluetooth

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// notifyState caches notify-send availability. It is resolved once via
// resolveNotifyState and then read without locking.
var notifyState struct {
	once      sync.Once
	available bool
	path      string
}

// notifyVerbose controls whether notify-send failures are logged to stderr.
var notifyVerbose bool

// SetVerbose controls whether notify logs failures to stderr. main.go calls
// this after parsing the -v flag.
func SetVerbose(v bool) { notifyVerbose = v }

// resolveNotifyState performs the one-time notify-send PATH lookup. It must be
// the only place that resolves availability so the sync.Once is never consumed
// by a no-op (which would permanently disable notifications).
func resolveNotifyState() {
	notifyState.once.Do(func() {
		// resolveBinary falls back to absolute prefixes when PATH is stripped
		// (i3/polybar launches). notify-send absence is not an error.
		if p, err := resolveBinary(notifySendExecutable); err == nil {
			notifyState.available = true
			notifyState.path = p
		}
	})
}

// notify sends a desktop status notification asynchronously. It is a no-op when
// notify-send is not installed.
func notify(title, body string) {
	notifyWithDuration(title, body, notifyDurationStatus)
}

// notifyAlert sends a higher-attention notification (pairing requests).
func notifyAlert(title, body string) {
	notifyWithDuration(title, body, notifyDurationAlert)
}

func notifyWithDuration(title, body string, durationMs int) {
	resolveNotifyState()
	if !notifyState.available {
		return
	}
	// Flags precede the summary/body positionals; some notify-send builds are
	// strict about argument order.
	args := []string{
		"-a", notifyAppName,
		"-t", fmt.Sprintf("%d", durationMs),
		"-i", notifyIconBluetooth,
		title,
		body,
	}
	go func() {
		cmd := exec.Command(notifyState.path, args...)
		// notify-send reaches the notification daemon over the session bus;
		// an i3 keybind launch may lack DBUS_SESSION_BUS_ADDRESS, so supply it.
		cmd.Env = notifyEnv()
		if err := cmd.Run(); err != nil && notifyVerbose {
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
	path, err := resolveBinary("systemctl")
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
	resolveNotifyState()
	if !notifyState.available {
		return false
	}
	out, _ := exec.Command(notifyState.path, "--help").CombinedOutput()
	return bytes.Contains(out, []byte("--print-id"))
}
