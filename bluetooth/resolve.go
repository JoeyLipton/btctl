// Package bluetooth - resolve.go locates external executables. When btctl is
// launched from an i3 keybind or a polybar click, it inherits a stripped PATH
// (often just /usr/lib/go/bin), so a bare exec.LookPath fails for notify-send,
// rofi, dmenu, zenity, and rfkill. resolveBinary is the single chokepoint for
// binary resolution: it tries PATH first, then falls back to known absolute
// directory prefixes. Every external-binary lookup in the package goes through
// it so a stripped PATH never silently disables a feature.
package bluetooth

import (
	"fmt"
	"os"
	"os/exec"
)

// knownPrefixes are the absolute directory prefixes tried in order when
// exec.LookPath fails due to a stripped PATH. These cover every major Linux
// distribution layout. /usr/bin covers Arch, Debian, Ubuntu, Fedora.
// /usr/local/bin covers user-installed binaries. /bin covers systems where
// /bin is not a symlink to /usr/bin.
var knownPrefixes = []string{
	"/usr/bin",
	"/usr/local/bin",
	"/bin",
	"/usr/sbin",
	"/usr/local/sbin",
}

// resolveBinary finds the absolute path of a named executable. It first calls
// exec.LookPath which respects the process PATH; if that fails (common when
// launched from i3 or polybar with a stripped PATH), it probes each directory
// in knownPrefixes with os.Stat. Returns a wrapped ErrBinaryNotFound on failure.
func resolveBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, prefix := range knownPrefixes {
		candidate := prefix + "/" + name
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrBinaryNotFound, name)
}
