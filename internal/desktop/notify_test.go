package desktop

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/joeylipton/btctl/internal/testenv"
)

func TestMain(m *testing.M) {
	// Never start real desktop programs (notifications, zenity, ...).
	cleanup, err := testenv.IsolatePATH()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// When BTCTL_NOTIFY_CHILD is set, this test binary sends one notification
// and exits immediately, like `btctl disconnect` does after reporting.
func TestNotifyChildProcess(t *testing.T) {
	if os.Getenv("BTCTL_NOTIFY_CHILD") == "" {
		t.Skip("helper process for TestNotifyIsNotLostOnExit")
	}
	// TestMain put do-nothing stubs first on PATH; put the fake before them.
	os.Setenv("PATH", os.Getenv("BTCTL_NOTIFY_FAKE_DIR")+string(os.PathListSeparator)+os.Getenv("PATH"))
	notifyState = &notifyResolver{}
	Notify("Disconnected from Speaker")
	os.Exit(0)
}

// Notifications used to be started from a goroutine, so a CLI command that
// exited right after notifying usually lost them.
func TestNotifyIsNotLostOnExit(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "notifications")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNotifyChildProcess$")
		cmd.Env = append(os.Environ(), "BTCTL_NOTIFY_CHILD=1", "BTCTL_NOTIFY_FAKE_DIR="+dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %v\n%s", err, output)
		}
	}

	// notify-send outlives its parent; give the last one a moment to write.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(out)
		n := strings.Count(string(data), "Disconnected from Speaker")
		if n == 5 {
			if !strings.Contains(string(data), "-a btctl") {
				t.Errorf("notification not tagged with the app name: %s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 5 notifications delivered:\n%s", n, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStderrProgress(t *testing.T) {
	var buf bytes.Buffer
	p := &stderrProgress{w: &buf}
	p.Start(10)
	p.DeviceFound("Bose QC Earbuds — Renée’s pair", "AA:BB:CC:DD:EE:FF")
	p.Complete(1)
	out := buf.String()
	for _, want := range []string{"scanning for 10 seconds", "found:   Bose QC Earbuds — Renée’ AA:BB:CC:DD:EE:FF", "scan complete. 1 device(s) found."} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output lacks %q:\n%s", want, out)
		}
	}
	if !utf8.ValidString(out) {
		t.Errorf("progress output is not valid UTF-8: %q", out)
	}
}
