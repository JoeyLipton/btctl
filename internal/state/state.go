// Package state persists the small amount of state btctl keeps between runs:
// currently only the adapter chosen with `btctl adapter select`.
//
// State lives in $XDG_STATE_HOME/btctl (default ~/.local/state/btctl), as the
// XDG Base Directory specification recommends for data that should survive
// restarts but is not configuration.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joeylipton/btctl/internal/bluez"
)

const adapterFile = "adapter"

// Dir returns the directory btctl stores state in.
func Dir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "btctl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate state directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "btctl"), nil
}

// LoadAdapter returns the saved adapter name, or "" when none is saved.
func LoadAdapter() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, adapterFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read saved adapter: %w", err)
	}
	name := strings.TrimSpace(string(data))
	if !bluez.IsAdapterName(name) {
		return "", fmt.Errorf("saved adapter %q in %s is not an hciN name", name, filepath.Join(dir, adapterFile))
	}
	return name, nil
}

// SaveAdapter records name as the adapter future btctl runs should use.
func SaveAdapter(name string) error {
	if !bluez.IsAdapterName(name) {
		return fmt.Errorf("save adapter: %q is not an hciN name", name)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save adapter: %w", err)
	}
	// Write to a temporary file and rename so a crash never leaves a
	// half-written file behind.
	tmp, err := os.CreateTemp(dir, adapterFile+".*")
	if err != nil {
		return fmt.Errorf("save adapter: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(name + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("save adapter: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save adapter: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, adapterFile)); err != nil {
		return fmt.Errorf("save adapter: %w", err)
	}
	return nil
}
