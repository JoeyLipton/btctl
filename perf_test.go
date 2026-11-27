package main

import (
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// status is polled by status bars every few seconds; each GetManagedObjects
// returns every object BlueZ knows (often hundreds of cached BLE devices).
func TestCommandsFetchTheObjectTreeOnce(t *testing.T) {
	fake := newCLIFake(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Headphones", Paired: true, Connected: true, Battery: -1})

	for _, args := range [][]string{{"status"}, {"list"}, {"adapter", "status"}} {
		fake.ResetCalls()
		if code, _, stderr := runCLI(t, args...); code != exitSuccess {
			t.Fatalf("%v exited %d: %s", args, code, stderr)
		}
		if n := fake.Calls("GetManagedObjects"); n != 1 {
			t.Errorf("%v called GetManagedObjects %d times, want 1", args, n)
		}
	}
}

// New no longer probes BlueZ; the first call must still say what is wrong.
func TestBlueZNotRunning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", fakebluez.StartBus(t)) // a bus without org.bluez

	if code, stdout, stderr := runCLI(t, "status"); code != exitSuccess || stdout != "[BT] off\n" || stderr != "" {
		t.Errorf("status = %d %q %q, want [BT] off and nothing on stderr", code, stdout, stderr)
	}
	if code, _, stderr := runCLI(t, "list"); code != exitError || stderr != "error: BlueZ is not running\n" {
		t.Errorf("list = %d %q, want 'error: BlueZ is not running'", code, stderr)
	}
}
