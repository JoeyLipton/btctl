package menu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/fakebluez"
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

const testDevice = dbus.ObjectPath("/org/bluez/hci0/dev_AA_AA_AA_AA_AA_01")

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// notifications records what the menu would have shown as notifications.
type notifications struct {
	mu   sync.Mutex
	sent []string
}

func (n *notifications) add(s string) {
	n.mu.Lock()
	n.sent = append(n.sent, s)
	n.mu.Unlock()
}

func (n *notifications) all() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.sent, "\n")
}

// newTestMenu starts a fake bluetoothd with one powered adapter and returns
// it, a menu on a client connected to it, and the notifications the menu
// sends. The menu behaves as if opened from a keybinding (stderr unseen).
func newTestMenu(t *testing.T) (*fakebluez.BlueZ, *Menu, *notifications) {
	t.Helper()
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:11:22:33:44:55", true)
	client, err := bluez.New(bluez.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	m := New(client, Config{Backend: BackendRofi})
	n := &notifications{}
	m.stderrVisible = false
	m.notify = n.add
	m.alert = func(title, body string) { n.add(title + ": " + body) }
	return fake, m, n
}

// registerMenuAgent registers the menu's pairing handler as the agent and
// lets it answer for testDevice, as if the menu were connecting it.
func registerMenuAgent(t *testing.T, m *Menu) {
	t.Helper()
	agent := bluez.NewAgent(m.PairingHandler())
	if err := m.client.RegisterAgent(testContext(t), agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Expect(testDevice))
	t.Cleanup(func() { _ = m.client.UnregisterAgent(context.Background(), agent) })
}

// registerAgentOnly registers the menu's pairing handler as the agent without
// pre-authorising any device: the menu's own connect flow must do that.
func registerAgentOnly(t *testing.T, m *Menu) *bluez.Agent {
	t.Helper()
	agent := bluez.NewAgent(m.PairingHandler())
	if err := m.client.RegisterAgent(testContext(t), agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.client.UnregisterAgent(context.Background(), agent) })
	return agent
}

// fakePickerScript stands in for rofi and dmenu. Each invocation logs its
// arguments and input, then answers with the next step from the steps file:
// an extended regex selecting the first matching input line, TYPE:<text> to
// answer with free text, or ESC to exit 1 as if the user pressed Escape.
// FAKE_PICKER_SLEEP delays the answer.
const fakePickerScript = `#!/bin/sh
input=$(cat)
printf 'ARGS: %s\n%s\n---\n' "$*" "$input" >> "$FAKE_PICKER_LOG"
if [ -n "$FAKE_PICKER_SLEEP" ]; then sleep "$FAKE_PICKER_SLEEP"; fi
n=$(cat "$FAKE_PICKER_STEPS.n" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$FAKE_PICKER_STEPS.n"
step=$(sed -n "${n}p" "$FAKE_PICKER_STEPS")
if [ -z "$step" ] || [ "$step" = ESC ]; then exit 1; fi
case "$step" in TYPE:*) printf '%s\n' "${step#TYPE:}"; exit 0;; esac
line=$(printf '%s\n' "$input" | grep -m1 -E -- "$step") || exit 1
printf '%s\n' "$line"
`

// fakePicker installs fake rofi and dmenu executables at the front of PATH
// that answer with the given steps in order. It returns a function that
// reads everything the pickers were shown.
func fakePicker(t *testing.T, steps ...string) (shown func() string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"rofi", "dmenu"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fakePickerScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stepsFile := filepath.Join(dir, "steps")
	if err := os.WriteFile(stepsFile, []byte(strings.Join(steps, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PICKER_STEPS", stepsFile)
	t.Setenv("FAKE_PICKER_LOG", logFile)
	return func() string {
		data, err := os.ReadFile(logFile)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestFakePickerFollowsSteps(t *testing.T) {
	shown := fakePicker(t, "^b", "ESC")
	m := New(nil, Config{Backend: BackendRofi})
	if got, err := m.prompt(context.Background(), "first", []string{"a", "b", "c"}); err != nil || got != "b" {
		t.Fatalf("first prompt = %q, %v; want b", got, err)
	}
	if got, err := m.prompt(context.Background(), "second", []string{"a"}); err != nil || got != "" {
		t.Fatalf("second prompt = %q, %v; want Escape", got, err)
	}
	if log := shown(); !strings.Contains(log, "-p first") || !strings.Contains(log, "-p second") {
		t.Errorf("picker log missing prompts:\n%s", log)
	}
}
