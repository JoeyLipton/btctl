package textfmt

import (
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 24, "short"},
		{"exactly", 7, "exactly"},
		{"Bose QC Earbuds — Renée", 20, "Bose QC Earbuds — Re"},
		// Cutting at 24 bytes used to split "é" and produce invalid UTF-8.
		{"Bose QC Earbuds — Renée II", 22, "Bose QC Earbuds — René"},
		{"🎧🎧🎧", 2, "🎧🎧"},
		{"anything", 0, ""},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := Truncate(tt.in, tt.width)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Truncate(%q, %d) = %q is not valid UTF-8", tt.in, tt.width, got)
		}
	}
}
