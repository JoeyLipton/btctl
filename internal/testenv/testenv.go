// Package testenv isolates tests from the desktop they run on.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
)

// stubs are desktop helpers btctl may launch. Tests must never start the real
// ones: that would pop up notifications, dialogs or menus on the developer's
// screen, or change rfkill state.
var stubs = []string{"notify-send", "zenity", "rofi", "dmenu", "rfkill", "systemctl"}

// IsolatePATH puts do-nothing stand-ins for every desktop helper at the front
// of PATH and returns a cleanup function. Call it from TestMain. Individual
// tests may still prepend their own fakes.
func IsolatePATH() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "btctl-test-stubs-")
	if err != nil {
		return nil, err
	}
	for _, name := range stubs {
		// Exit 1, like a picker the user dismissed or a helper that failed.
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("write stub %s: %w", name, err)
		}
	}
	oldPath := os.Getenv("PATH")
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	// notify-send and zenity need a session bus; make sure none is reachable.
	oldBus, hadBus := os.LookupEnv("DBUS_SESSION_BUS_ADDRESS")
	_ = os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/btctl-test")
	return func() {
		_ = os.Setenv("PATH", oldPath)
		if hadBus {
			_ = os.Setenv("DBUS_SESSION_BUS_ADDRESS", oldBus)
		} else {
			_ = os.Unsetenv("DBUS_SESSION_BUS_ADDRESS")
		}
		_ = os.RemoveAll(dir)
	}, nil
}
