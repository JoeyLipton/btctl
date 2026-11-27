package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdapterRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if got, err := LoadAdapter(); err != nil || got != "" {
		t.Fatalf("LoadAdapter with nothing saved = %q, %v; want \"\", nil", got, err)
	}
	if err := SaveAdapter("hci1"); err != nil {
		t.Fatalf("SaveAdapter: %v", err)
	}
	if got, err := LoadAdapter(); err != nil || got != "hci1" {
		t.Fatalf("LoadAdapter = %q, %v; want hci1", got, err)
	}
	if err := SaveAdapter("hci0"); err != nil {
		t.Fatalf("SaveAdapter overwrite: %v", err)
	}
	if got, _ := LoadAdapter(); got != "hci0" {
		t.Fatalf("LoadAdapter after overwrite = %q, want hci0", got)
	}
}

func TestSaveAdapterRejectsInvalidNames(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, name := range []string{"", "hci", "eth0", "hci0/../x"} {
		if err := SaveAdapter(name); err == nil {
			t.Errorf("SaveAdapter(%q) succeeded, want error", name)
		}
	}
}

func TestLoadAdapterRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "btctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "btctl", "adapter"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAdapter(); err == nil {
		t.Error("LoadAdapter accepted a corrupt file")
	}
}

func TestDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "relative/path") // ignored: must be absolute
	t.Setenv("HOME", home)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "btctl"); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}
