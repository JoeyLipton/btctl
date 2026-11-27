package menu

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/joeylipton/btctl/internal/bluez"
)

// Picker lines are piped to rofi/dmenu; a name cut mid-character used to
// produce invalid UTF-8 ("Bose QC Earbuds — Ren\xc3").
func TestDeviceLineKeepsUTF8Valid(t *testing.T) {
	tests := []struct{ name, shown string }{
		// 23 characters but 27 bytes: fits the 24-column field.
		{"Bose QC Earbuds — Renée", "Bose QC Earbuds — Renée "},
		// Too long: cut to 24 characters, not 24 bytes.
		{"Bose QC Earbuds — Renée’s pair", "Bose QC Earbuds — Renée’ "},
	}
	for _, tt := range tests {
		d := bluez.Device{Name: tt.name, Address: "AA:BB:CC:DD:EE:FF", Paired: true, Battery: bluez.BatteryUnavailable}
		line := deviceLine(d, 0)
		if !utf8.ValidString(line) {
			t.Fatalf("deviceLine(%q) produced invalid UTF-8: %q", tt.name, line)
		}
		if !strings.Contains(line, tt.shown) || !strings.HasSuffix(line, "AA:BB:CC:DD:EE:FF") {
			t.Errorf("deviceLine(%q) = %q, want it to show %q", tt.name, line, tt.shown)
		}
		if _, ok := deviceForLine([]bluez.Device{d}, line); !ok {
			t.Errorf("line %q no longer maps back to its device", line)
		}
	}
}

func TestDeviceLineFormats(t *testing.T) {
	d := bluez.Device{Address: "AA:BB:CC:DD:EE:FF", Connected: true, Paired: true, Battery: 80}
	if got, want := deviceLine(d, 0), "[connected] (unknown)                AA:BB:CC:DD:EE:FF  80%"; got != want {
		t.Errorf("deviceLine = %q, want %q", got, want)
	}
	if got, want := deviceLine(d, 3), "  3) (unknown)                AA:BB:CC:DD:EE:FF  80%"; got != want {
		t.Errorf("numbered deviceLine = %q, want %q", got, want)
	}
}
