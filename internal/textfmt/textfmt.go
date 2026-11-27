// Package textfmt holds the small text helpers shared by the CLI and the
// interactive menu.
package textfmt

// Truncate shortens s to at most width characters. It counts runes, not
// bytes, so multi-byte characters (accents, curly quotes, emoji) in device
// names are never cut in half; fmt's %-*s padding also counts runes, so
// columns stay aligned.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == width {
			return s[:i]
		}
		n++
	}
	return s
}

// YesNo renders a bool as "yes" or "no".
func YesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
