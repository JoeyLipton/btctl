package bluetooth

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Picker lines are piped to rofi/dmenu; a name cut mid-character used to
// produce invalid UTF-8 ("Bose QC Earbuds — Ren\xc3").
func TestBuildDeviceLineKeepsUTF8Valid(t *testing.T) {
	tests := []struct{ name, shown string }{
		// 23 characters but 27 bytes: fits the 24-column field.
		{"Bose QC Earbuds — Renée", "Bose QC Earbuds — Renée "},
		// Too long: cut to 24 characters, not 24 bytes.
		{"Bose QC Earbuds — Renée’s pair", "Bose QC Earbuds — Renée’ "},
	}
	for _, tt := range tests {
		d := Device{Name: tt.name, Address: "AA:BB:CC:DD:EE:FF", Paired: true, Battery: batteryUnavailable}
		line := buildDeviceLine(d, 0, false)
		if !utf8.ValidString(line) {
			t.Fatalf("buildDeviceLine(%q) produced invalid UTF-8: %q", tt.name, line)
		}
		if !strings.Contains(line, tt.shown) || !strings.HasSuffix(line, "AA:BB:CC:DD:EE:FF") {
			t.Errorf("buildDeviceLine(%q) = %q, want it to show %q", tt.name, line, tt.shown)
		}
		if got := matchDeviceByAddress([]Device{d}, line); got == nil {
			t.Errorf("line %q no longer maps back to its device", line)
		}
	}
}
