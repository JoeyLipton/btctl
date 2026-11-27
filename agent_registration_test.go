package main

import (
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// Registering makes btctl BlueZ's default agent, so commands that never pair
// must not do it.
func TestOnlyPairingCommandsRegisterAnAgent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:00:00:00:00:00", true)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Speaker", Paired: true, Bonded: true, Battery: -1})

	for _, args := range [][]string{
		{"list"},
		{"status"},
		{"trust", "AA:AA:AA:AA:AA:01"},
		{"disconnect", "AA:AA:AA:AA:AA:01"},
		{"adapter", "status"},
	} {
		fake.ResetCalls()
		if code, _, stderr := runCLI(t, args...); code != exitSuccess {
			t.Fatalf("%v exited %d: %s", args, code, stderr)
		}
		if n := fake.Calls("AgentManager1.RegisterAgent"); n != 0 {
			t.Errorf("%v registered an agent %d time(s)", args, n)
		}
	}

	fake.ResetCalls()
	if code, _, stderr := runCLI(t, "connect", "AA:AA:AA:AA:AA:01"); code != exitSuccess {
		t.Fatalf("connect exited %d: %s", code, stderr)
	}
	if n := fake.Calls("AgentManager1.RegisterAgent"); n != 1 {
		t.Errorf("connect registered an agent %d time(s), want 1", n)
	}
	if fake.Agent() != nil {
		t.Error("connect left its agent registered")
	}
}
