package bluetooth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// When BTCTL_NOTIFY_CHILD is set, this test binary sends one notification
// and exits immediately, like `btctl disconnect` does after reporting.
func TestNotifyChildProcess(t *testing.T) {
	if os.Getenv("BTCTL_NOTIFY_CHILD") == "" {
		t.Skip("helper process for TestNotifyIsNotLostOnExit")
	}
	notifyState = &notifyResolver{}
	notify("btctl", "Disconnected from Speaker")
	os.Exit(0)
}

// Notifications were started from a goroutine, so a CLI command that exited
// right after calling notify usually lost them.
func TestNotifyIsNotLostOnExit(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "notifications")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNotifyChildProcess$")
		cmd.Env = append(os.Environ(),
			"BTCTL_NOTIFY_CHILD=1",
			"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent", // never reach a real desktop
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %v\n%s", err, output)
		}
	}

	// notify-send outlives its parent; give the last one a moment to write.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(out)
		if n := strings.Count(string(data), "Disconnected from Speaker"); n == 5 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("%d of 5 notifications delivered:\n%s", n, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
