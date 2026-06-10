// Package bluetooth - errors.go declares the package's error sentinels and the
// FormatUserError helper that turns raw D-Bus errors into plain-English lines.
// Sentinels are compared with errors.Is by callers (main.go, picker.go) so user
// messages and exit codes can be chosen without string matching.
package bluetooth

import (
	"errors"
	"strings"
)

// Package-level error sentinels for errors checked by value with errors.Is.
var (
	ErrAgentCanceled     = errors.New("pairing canceled by BlueZ")
	ErrNoPicker          = errors.New("no picker found: install rofi or dmenu")
	ErrNoAdapter         = errors.New("no Bluetooth adapter found")
	ErrDeviceNotFound    = errors.New("device not found")
	ErrAdapterNotReady   = errors.New("adapter not ready")
	ErrConnectInProgress = errors.New("connection already in progress")

	// ErrBinaryNotFound is returned by resolveBinary when an external
	// executable cannot be found via PATH or known absolute prefixes.
	ErrBinaryNotFound = errors.New("executable not found")
)

// dbusErrorPhrases maps raw BlueZ / D-Bus error names to plain-English phrases.
// btctl runs in a desktop environment, so the person reading an error (in a
// status bar, terminal, or notification) should never see a D-Bus error name.
var dbusErrorPhrases map[string]string

func init() {
	dbusErrorPhrases = map[string]string{
		errNameNotReady:       "adapter not ready",
		errNameFailed:         "connection failed",
		errNameAlreadyExists:  "device already paired",
		errNameNotSupported:   "not supported by device",
		errNameAuthFailed:     "authentication failed",
		errNameAuthCanceled:   "pairing canceled",
		errNameInProgress:     "operation already in progress",
		errNameServiceUnknown: "BlueZ is not running",
		errNameNoReply:        "BlueZ did not respond",
	}
}

// FormatUserError converts an error into a single, human-readable line. When
// the error carries a known D-Bus error name, that name is replaced with a
// readable phrase; otherwise the raw error string is returned unchanged so
// sentinel messages (e.g. "device not found: ...") pass through.
func FormatUserError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for name, phrase := range dbusErrorPhrases {
		if strings.Contains(msg, name) {
			return phrase
		}
	}
	return msg
}
