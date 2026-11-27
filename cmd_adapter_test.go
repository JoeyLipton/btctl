package main

import (
	"strings"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

func TestAdapterSelectIsRemembered(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:00:00:00:00:00", true)
	fake.AddAdapter("hci1", "11:11:11:11:11:11", true)

	if code, _, stderr := runCLI(t, "adapter", "select", "hci1"); code != exitSuccess {
		t.Fatalf("adapter select hci1 exited %d: %s", code, stderr)
	}
	// A new process (a fresh client) must still use hci1.
	code, stdout, stderr := runCLI(t, "adapter", "status")
	if code != exitSuccess || !strings.Contains(stdout, "hci1") {
		t.Fatalf("adapter status after select = %d %q %q, want hci1", code, stdout, stderr)
	}
	// -a overrides the remembered adapter for one run.
	if _, stdout, _ := runCLI(t, "-a", "hci0", "adapter", "status"); !strings.Contains(stdout, "hci0") {
		t.Errorf("-a hci0 adapter status = %q, want hci0", stdout)
	}
	_, stdout, _ = runCLI(t, "adapter", "list")
	if !strings.Contains(stdout, "hci1  11:11:11:11:11:11  powered  (active)") {
		t.Errorf("adapter list does not mark hci1 active:\n%s", stdout)
	}
}

func TestAdapterSelectUnknownAdapter(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:00:00:00:00:00", true)

	code, _, stderr := runCLI(t, "adapter", "select", "hci9")
	if code != exitError || !strings.Contains(stderr, "no such adapter: hci9") {
		t.Errorf("adapter select hci9 = %d %q, want exit 1 and 'no such adapter'", code, stderr)
	}
	if code, _, stderr := runCLI(t, "-a", "hci9", "list"); code != exitError || !strings.Contains(stderr, "no such adapter") {
		t.Errorf("-a hci9 list = %d %q, want exit 1", code, stderr)
	}
}
