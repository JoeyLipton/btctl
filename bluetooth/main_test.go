package bluetooth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Never pop up real desktop notifications from tests.
	notifyState = disabledNotifier()
	os.Exit(m.Run())
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
	m := NewMenu(nil, PickerConfig{Backend: BackendRofi})
	if got, err := m.prompt("first", []string{"a", "b", "c"}); err != nil || got != "b" {
		t.Fatalf("first prompt = %q, %v; want b", got, err)
	}
	if got, err := m.prompt("second", []string{"a"}); err != nil || got != "" {
		t.Fatalf("second prompt = %q, %v; want Escape", got, err)
	}
	if log := shown(); !strings.Contains(log, "-p first") || !strings.Contains(log, "-p second") {
		t.Errorf("picker log missing prompts:\n%s", log)
	}
}
