package bluez

import (
	"fmt"
	"os"
	"testing"

	"github.com/joeylipton/btctl/internal/testenv"
)

func TestMain(m *testing.M) {
	// Never start real desktop programs (notifications, rofi, zenity, ...).
	cleanup, err := testenv.IsolatePATH()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
