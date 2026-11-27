// Package execpath locates the external programs btctl runs (rofi, dmenu,
// notify-send, zenity, rfkill, systemctl).
//
// When btctl is launched from an i3 keybinding or a polybar click it inherits
// a stripped PATH, so a bare exec.LookPath can fail for programs that are
// installed. Find tries PATH first and then the standard binary directories,
// so a stripped PATH never silently disables a feature.
package execpath

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ErrNotFound is returned when a program is neither on PATH nor in one of
// the standard directories.
var ErrNotFound = errors.New("executable not found")

// fallbackDirs are tried in order when exec.LookPath fails. /usr/bin covers
// Arch, Debian, Ubuntu and Fedora; /usr/local/bin covers user-installed
// programs; /bin covers systems where it is not a symlink to /usr/bin.
var fallbackDirs = []string{
	"/usr/bin",
	"/usr/local/bin",
	"/bin",
	"/usr/sbin",
	"/usr/local/sbin",
}

// Find returns the absolute path of the named program.
func Find(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range fallbackDirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, name)
}
