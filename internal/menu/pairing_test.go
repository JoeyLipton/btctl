package menu

import (
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

func isDBusError(err error, name string) bool {
	e, ok := err.(dbus.Error)
	return ok && e.Name == name
}

func TestPairingPromptsUser(t *testing.T) {
	for _, tt := range []struct {
		answer string
		wantOK bool
	}{
		{"^yes", true},
		{"^no", false},
	} {
		t.Run(tt.answer, func(t *testing.T) {
			fake, m, sent := newTestMenu(t)
			fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Phone", Battery: -1})
			shown := fakePicker(t, tt.answer)
			registerMenuAgent(t, m)

			_, err := fake.CallAgent(testContext(t), "RequestConfirmation", testDevice, uint32(7))
			if (err == nil) != tt.wantOK {
				t.Fatalf("RequestConfirmation = %v, want accepted=%v", err, tt.wantOK)
			}
			log := shown()
			for _, want := range []string{"Pairing request - Phone", "Passkey: 000007"} {
				if !strings.Contains(log, want) {
					t.Errorf("picker was not shown %q:\n%s", want, log)
				}
			}
			if !strings.Contains(sent.all(), "Pair with Phone?") {
				t.Errorf("no pairing alert; sent: %s", sent.all())
			}
		})
	}
}

func TestPairingPromptClosesOnCancel(t *testing.T) {
	fake, m, _ := newTestMenu(t)
	fakePicker(t, "^yes")
	t.Setenv("FAKE_PICKER_SLEEP", "30")
	registerMenuAgent(t, m)

	ctx := testContext(t)
	errCh := make(chan error, 1)
	go func() {
		_, err := fake.CallAgent(ctx, "RequestConfirmation", testDevice, uint32(7))
		errCh <- err
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if err := fake.CancelAgent(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !isDBusError(err, "org.bluez.Error.Canceled") {
			t.Errorf("request = %v, want Canceled", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("picker took %s to close after Cancel", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("picker was not closed when BlueZ cancelled the request")
	}
}

// End to end: pairing a device that asks for its passkey, typed into the menu.
func TestPasskeyEntryPairsDevice(t *testing.T) {
	fake, m, _ := newTestMenu(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Keyboard", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "RequestPasskey", Passkey: 654321})
	shown := fakePicker(t, "TYPE:654321")
	registerMenuAgent(t, m)

	if err := m.client.Pair(testContext(t), path); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if !strings.Contains(shown(), "Enter the passkey shown on Keyboard") {
		t.Errorf("picker did not ask for the passkey:\n%s", shown())
	}
}

// Connecting from the menu pairs a device found by a scan and reports it.
func TestMenuScanConnectsNewDevice(t *testing.T) {
	fake, m, sent := newTestMenu(t)
	fake.OnStartDiscovery(func(b *fakebluez.BlueZ) {
		time.Sleep(50 * time.Millisecond)
		p := b.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Headset", RSSI: -40, Battery: -1})
		b.SetBehavior(p, fakebluez.Behavior{AgentRequest: "RequestConfirmation", Passkey: 1})
	})
	fakePicker(t,
		"Scan for new devices",
		"TYPE:1", // a typed duration: 1 second
		"Headset",
		"^yes", // the pairing prompt
		"ESC",  // leave the main menu
	)
	registerAgentOnly(t, m)

	if err := m.Run(testContext(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(sent.all(), "Connected to Headset") {
		t.Errorf("no 'Connected to Headset' notification; sent:\n%s", sent.all())
	}
	path := fakebluez.DevicePath("hci0", "AA:AA:AA:AA:AA:01")
	for _, prop := range []string{"Paired", "Connected"} {
		if v, _ := fake.Property(path, fakebluez.DeviceIface, prop).(bool); !v {
			t.Errorf("%s = false after connecting from the menu", prop)
		}
	}
}
