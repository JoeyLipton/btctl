package menu

import (
	"strings"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// Opened from a keybinding, the menu's stderr goes nowhere; outcomes must
// reach the user as notifications.
func TestMenuOutcomesAreNotified(t *testing.T) {
	fake, m, sent := newTestMenu(t)
	speaker := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Speaker", Paired: true, Battery: -1})
	fakePicker(t,
		"Disconnect a device", // nothing is connected
		"Device info", "Speaker", "^> trust this device",
		"ESC", // leave device info
		"Adapter settings", "Power off", "ESC",
		"ESC", // leave the main menu
	)

	if err := m.Run(testContext(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"No connected devices", "Trusted Speaker (AA:AA:AA:AA:AA:01)", "Adapter powered off"} {
		if !strings.Contains(sent.all(), want) {
			t.Errorf("no notification containing %q; sent:\n%s", want, sent.all())
		}
	}
	if trusted, _ := fake.Property(speaker, fakebluez.DeviceIface, "Trusted").(bool); !trusted {
		t.Error("device was not trusted")
	}
	if powered, _ := fake.Property(fakebluez.AdapterPath("hci0"), fakebluez.AdapterIface, "Powered").(bool); powered {
		t.Error("adapter was not powered off")
	}
}

// From a terminal, stderr is visible and notifications would be duplicates.
func TestMenuDoesNotNotifyInATerminal(t *testing.T) {
	_, m, sent := newTestMenu(t)
	m.stderrVisible = true
	fakePicker(t, "Disconnect a device", "ESC")

	if err := m.Run(testContext(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sent.all(); got != "" {
		t.Errorf("notified although stderr is a terminal:\n%s", got)
	}
}

func TestMenuForgetAsksFirst(t *testing.T) {
	fake, m, _ := newTestMenu(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Old Mouse", Paired: true, Battery: -1})
	fakePicker(t, "Forget a device", "Old Mouse", "^no", "Forget a device", "Old Mouse", "^yes", "ESC")

	if err := m.Run(testContext(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.HasObject(path) {
		t.Error("device still known after confirming Forget")
	}
	if n := fake.Calls("Adapter1.RemoveDevice"); n != 1 {
		t.Errorf("RemoveDevice called %d times, want 1 (the first answer was no)", n)
	}
}

func TestParseScanDuration(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{" 5 seconds", 5, false},
		{"60 seconds", 60, false},
		{"42", 42, false},
		{"0", 0, true},
		{"999", 0, true}, // typed values obey the same limit as `scan -t`
		{"soon", 0, true},
		{"", 0, true},
	} {
		got, err := parseScanDuration(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseScanDuration(%q) = %d, %v; want %d, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
