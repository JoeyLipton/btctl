// Package bluetooth - errors.go declares the package's error sentinels, the
// D-Bus error names btctl reacts to, and FormatUserError, which turns raw
// D-Bus errors into plain-English lines. Sentinels are compared with
// errors.Is and D-Bus errors by their error name (never by message text,
// which is free-form and differs between BlueZ versions).
package bluetooth

import (
	"context"
	"errors"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Package-level error sentinels for errors checked by value with errors.Is.
var (
	ErrAgentCanceled     = errors.New("pairing canceled by BlueZ")
	ErrNoPicker          = errors.New("no picker found: install rofi or dmenu")
	ErrNoAdapter         = errors.New("no Bluetooth adapter found")
	ErrDeviceNotFound    = errors.New("device not found")
	ErrAdapterNotReady   = errors.New("adapter not ready")
	ErrAdapterNotFound   = errors.New("no such adapter")
	ErrConnectInProgress = errors.New("connection already in progress")

	// ErrBinaryNotFound is returned by resolveBinary when an external
	// executable cannot be found via PATH or known absolute prefixes.
	ErrBinaryNotFound = errors.New("executable not found")
)

// D-Bus error names returned by BlueZ and the bus daemon.
const (
	errNameRejected          = "org.bluez.Error.Rejected"
	errNameCanceled          = "org.bluez.Error.Canceled"
	errNameNotReady          = "org.bluez.Error.NotReady"
	errNameFailed            = "org.bluez.Error.Failed"
	errNameAlreadyExists     = "org.bluez.Error.AlreadyExists"
	errNameAlreadyConnected  = "org.bluez.Error.AlreadyConnected"
	errNameNotConnected      = "org.bluez.Error.NotConnected"
	errNameDoesNotExist      = "org.bluez.Error.DoesNotExist"
	errNameNotSupported      = "org.bluez.Error.NotSupported"
	errNameNotAvailable      = "org.bluez.Error.NotAvailable"
	errNameBlocked           = "org.bluez.Error.Blocked"
	errNameAuthFailed        = "org.bluez.Error.AuthenticationFailed"
	errNameAuthCanceled      = "org.bluez.Error.AuthenticationCanceled"
	errNameAuthRejected      = "org.bluez.Error.AuthenticationRejected"
	errNameAuthTimeout       = "org.bluez.Error.AuthenticationTimeout"
	errNameConnAttemptFailed = "org.bluez.Error.ConnectionAttemptFailed"
	errNameInProgress        = "org.bluez.Error.InProgress"
	errNameServiceUnknown    = "org.freedesktop.DBus.Error.ServiceUnknown"
	errNameNoReply           = "org.freedesktop.DBus.Error.NoReply"
	errNameAccessDenied      = "org.freedesktop.DBus.Error.AccessDenied"
	errNameInvalidArgs       = "org.freedesktop.DBus.Error.InvalidArgs"
	errNameUnknownProperty   = "org.freedesktop.DBus.Error.UnknownProperty"
)

// dbusErrorPhrases maps D-Bus error names to plain-English phrases. btctl runs
// in a desktop environment, so the person reading an error (in a status bar,
// terminal, or notification) should never see a D-Bus error name.
var dbusErrorPhrases = map[string]string{
	errNameNotReady:          "adapter not ready (is it powered on?)",
	errNameFailed:            "connection failed",
	errNameAlreadyExists:     "device already paired",
	errNameAlreadyConnected:  "device already connected",
	errNameNotConnected:      "device not connected",
	errNameDoesNotExist:      "device not found",
	errNameNotSupported:      "not supported by device",
	errNameNotAvailable:      "not available",
	errNameBlocked:           "adapter is blocked by rfkill",
	errNameAuthFailed:        "authentication failed",
	errNameAuthCanceled:      "pairing canceled",
	errNameAuthRejected:      "pairing rejected",
	errNameAuthTimeout:       "pairing timed out",
	errNameConnAttemptFailed: "connection attempt failed",
	errNameInProgress:        "operation already in progress",
	errNameServiceUnknown:    "BlueZ is not running",
	errNameNoReply:           "BlueZ did not respond",
	errNameAccessDenied:      "permission denied talking to BlueZ (check your D-Bus/polkit permissions)",
}

// dbusError extracts the D-Bus error carried by err, if any. godbus delivers
// error replies as dbus.Error values; errors created locally are often
// *dbus.Error, so both forms are accepted.
func dbusError(err error) (dbus.Error, bool) {
	var value dbus.Error
	if errors.As(err, &value) {
		return value, true
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil {
		return *ptr, true
	}
	return dbus.Error{}, false
}

// dbusErrorName returns the D-Bus error name carried by err, or "".
func dbusErrorName(err error) string {
	if e, ok := dbusError(err); ok {
		return e.Name
	}
	return ""
}

// dbusErrorMessage returns the human-readable message of a D-Bus error, or "".
func dbusErrorMessage(err error) string {
	if e, ok := dbusError(err); ok && len(e.Body) > 0 {
		if s, ok := e.Body[0].(string); ok {
			return s
		}
	}
	return ""
}

// isDBusError reports whether err (or any error it wraps) is a D-Bus error
// with one of the given names.
func isDBusError(err error, names ...string) bool {
	name := dbusErrorName(err)
	if name == "" {
		return false
	}
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}

// FormatUserError converts an error into a single, human-readable line. Known
// D-Bus errors become a plain-English phrase; for generic BlueZ failures the
// reason BlueZ gave (e.g. "br-connection-page-timeout") is kept because it is
// the only hint about what went wrong. Other errors pass through unchanged so
// sentinel messages (e.g. "device not found") reach the user.
func FormatUserError(err error) string {
	if err == nil {
		return ""
	}
	if phrase, ok := dbusErrorPhrases[dbusErrorName(err)]; ok {
		if dbusErrorName(err) == errNameFailed {
			if reason := strings.TrimSpace(dbusErrorMessage(err)); reason != "" && !strings.EqualFold(reason, "failed") {
				return phrase + ": " + reason
			}
		}
		return phrase
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out waiting for BlueZ"
	}
	return err.Error()
}
