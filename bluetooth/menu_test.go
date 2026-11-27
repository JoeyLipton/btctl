package bluetooth

import (
	"strings"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// Opened from a keybinding, the menu's stderr goes nowhere; outcomes must
// reach the user as notifications.
func TestMenuOutcomesAreNotified(t *testing.T) {
	fake, client := newTestClient(t)
	speaker := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Speaker", Paired: true, Battery: -1})
	sent := captureNotifications(t)
	fakePicker(t,
		"Disconnect a device", // nothing is connected
		"Device info", "Speaker", "^> trust this device",
		"ESC", // leave device info
		"Adapter settings", "Power off", "ESC",
		"ESC", // leave the main menu
	)

	if err := NewMenu(client, PickerConfig{Backend: BackendRofi}).Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"No connected devices", "Trusted Speaker (AA:AA:AA:AA:AA:01)", "Adapter powered off"} {
		waitForNotification(t, sent, want)
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
	_, client := newTestClient(t)
	sent := captureNotifications(t)
	stderrIsTerminal = func() bool { return true }
	fakePicker(t, "Disconnect a device", "ESC")

	if err := NewMenu(client, PickerConfig{Backend: BackendRofi}).Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sent(); strings.Contains(got, "No connected devices") {
		t.Errorf("notified although stderr is a terminal:\n%s", got)
	}
}
