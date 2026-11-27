package main

import (
	"strings"
	"testing"
	"time"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

func newCLIFake(t *testing.T) *fakebluez.BlueZ {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:00:00:00:00:00", true)
	return fake
}

func TestConnectPairsDeviceFoundByScan(t *testing.T) {
	fake := newCLIFake(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Name: "New Speaker", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "RequestConfirmation", Passkey: 123456})

	code, stdout, stderr := runCLI(t, "connect", "aa:aa:aa:aa:aa:02")
	if code != exitSuccess {
		t.Fatalf("connect exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, want := range []string{"pairing with New Speaker", "connecting to New Speaker", "confirming pairing with AA:AA:AA:AA:AA:02 (passkey 123456)"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stdout, "Connected to New Speaker (AA:AA:AA:AA:AA:02)") {
		t.Errorf("stdout = %q", stdout)
	}
	for _, prop := range []string{"Paired", "Trusted", "Connected"} {
		if v, _ := fake.Property(path, fakebluez.DeviceIface, prop).(bool); !v {
			t.Errorf("%s = false after connect", prop)
		}
	}

	if _, stdout, _ := runCLI(t, "connect", "AA:AA:AA:AA:AA:02"); !strings.Contains(stdout, "already connected") {
		t.Errorf("second connect = %q, want 'already connected'", stdout)
	}
}

func TestConnectUnknownDevice(t *testing.T) {
	newCLIFake(t)
	code, _, stderr := runCLI(t, "connect", "AA:AA:AA:AA:AA:99")
	if code != exitError || !strings.Contains(stderr, "device not found: AA:AA:AA:AA:AA:99") || !strings.Contains(stderr, "btctl scan") {
		t.Errorf("connect unknown = %d %q, want exit 1 with a scan hint", code, stderr)
	}
}

func TestPairCommand(t *testing.T) {
	fake := newCLIFake(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:03", Name: "Keyboard", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "DisplayPasskey", Passkey: 4321})

	code, stdout, stderr := runCLI(t, "pair", "AA:AA:AA:AA:AA:03")
	if code != exitSuccess || !strings.Contains(stdout, "Paired with Keyboard (AA:AA:AA:AA:AA:03)") {
		t.Fatalf("pair = %d %q %q", code, stdout, stderr)
	}
	// A keyboard pairs by typing the code btctl shows.
	if !strings.Contains(stderr, "Type 004321 on AA:AA:AA:AA:AA:03") {
		t.Errorf("pairing code not shown:\n%s", stderr)
	}
	if v, _ := fake.Property(path, fakebluez.DeviceIface, "Paired").(bool); !v {
		t.Error("device not paired")
	}
	if n := fake.Calls("Device1.Connect"); n != 0 {
		t.Errorf("pair called Device1.Connect %d time(s)", n)
	}
	if _, stdout, _ := runCLI(t, "pair", "AA:AA:AA:AA:AA:03"); !strings.Contains(stdout, "already paired") {
		t.Errorf("second pair = %q, want 'already paired'", stdout)
	}
}

func TestDisconnectCommand(t *testing.T) {
	fake := newCLIFake(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:04", Name: "Speaker", Paired: true, Bonded: true, Connected: true, Battery: -1})

	if code, stdout, stderr := runCLI(t, "disconnect", "AA:AA:AA:AA:AA:04"); code != exitSuccess || !strings.Contains(stdout, "Disconnected from Speaker") {
		t.Fatalf("disconnect = %d %q %q", code, stdout, stderr)
	}
	if v, _ := fake.Property(path, fakebluez.DeviceIface, "Connected").(bool); v {
		t.Error("device still connected")
	}
	if _, stdout, _ := runCLI(t, "disconnect", "AA:AA:AA:AA:AA:04"); !strings.Contains(stdout, "is not connected") {
		t.Errorf("second disconnect = %q", stdout)
	}
}

// The scan prompt used to call Connect without pairing; it now shares
// connect's pair-then-connect path.
func TestScanPromptPairsAndConnects(t *testing.T) {
	fake := newCLIFake(t)
	fake.OnStartDiscovery(func(b *fakebluez.BlueZ) {
		time.Sleep(50 * time.Millisecond)
		p := b.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:05", Name: "Fresh Headset", RSSI: -40, Battery: -1})
		b.SetBehavior(p, fakebluez.Behavior{AgentRequest: "RequestAuthorization"})
	})

	code, stdout, stderr := runCLIInput(t, "1\n", "scan", "-t", "1")
	if code != exitSuccess {
		t.Fatalf("scan exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, want := range []string{"found: Fresh Headset  AA:AA:AA:AA:AA:05", "1) Fresh Headset", "Connected to Fresh Headset (AA:AA:AA:AA:AA:05)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	path := fakebluez.DevicePath("hci0", "AA:AA:AA:AA:AA:05")
	for _, prop := range []string{"Paired", "Connected"} {
		if v, _ := fake.Property(path, fakebluez.DeviceIface, prop).(bool); !v {
			t.Errorf("%s = false after the scan prompt", prop)
		}
	}
}

func TestListVerboseFlagAfterCommand(t *testing.T) {
	fake := newCLIFake(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:06", Name: "Mouse", Paired: true, Trusted: true, Battery: 55})

	code, stdout, stderr := runCLI(t, "list", "-v")
	if code != exitSuccess {
		t.Fatalf("list -v exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Mouse") || !strings.Contains(stdout, "55%") || !strings.Contains(stdout, "trusted") {
		t.Errorf("list -v output lacks the trusted column:\n%s", stdout)
	}
	if !strings.Contains(stderr, "[v] call:") {
		t.Errorf("-v after the command did not enable verbose logging:\n%s", stderr)
	}
}

func TestStatusOutput(t *testing.T) {
	fake := newCLIFake(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:07", Name: "Headphones", Paired: true, Connected: true, Battery: -1})
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:08", Name: "Mouse", Paired: true, Battery: -1})

	code, stdout, stderr := runCLI(t, "status")
	if code != exitSuccess || stdout != "[BT] 1\nHeadphones\tAA:AA:AA:AA:AA:07\n" || stderr != "" {
		t.Errorf("status = %d %q %q", code, stdout, stderr)
	}
	if _, _, stderr := runCLI(t, "-v", "status"); stderr == "" {
		t.Error("-v status wrote nothing to stderr; -v used to be silently ignored for status")
	}
}
