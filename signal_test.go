package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildBinary compiles btctl once per test that needs real signal handling.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "btctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// interruptWhen starts bin with args, waits until ready() holds, sends
// SIGINT and returns the exit code, the output and how long the exit took.
func interruptWhen(t *testing.T, bin string, ready func() bool, args ...string) (code int, output string, took time.Duration) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("btctl %v never became ready; output:\n%s", args, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	took = time.Since(start)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	return code, out.String(), took
}

func TestInterruptStopsDiscovery(t *testing.T) {
	fake := newCLIFake(t)
	bin := buildBinary(t)

	code, out, took := interruptWhen(t, bin, func() bool { return fake.Calls("Adapter1.StartDiscovery") > 0 }, "scan", "-t", "60")
	if code != exitInterrupted {
		t.Errorf("exit code %d, want %d; output:\n%s", code, exitInterrupted, out)
	}
	if took > 3*time.Second {
		t.Errorf("btctl took %s to exit after Ctrl-C", took)
	}
	if n := fake.Calls("Adapter1.StopDiscovery"); n != 1 {
		t.Errorf("StopDiscovery called %d times, want 1", n)
	}
	if fake.Agent() != nil {
		t.Error("the pairing agent was left registered")
	}
}

func TestInterruptClosesThePicker(t *testing.T) {
	fake := newCLIFake(t)
	bin := buildBinary(t)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := "#!/bin/sh\ntouch " + started + "\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "rofi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ready := func() bool { _, err := os.Stat(started); return err == nil && fake.Agent() != nil }
	code, out, took := interruptWhen(t, bin, ready, "pick", "--rofi")
	if code != exitInterrupted {
		t.Errorf("exit code %d, want %d; output:\n%s", code, exitInterrupted, out)
	}
	if took > 3*time.Second {
		t.Errorf("btctl took %s to exit after Ctrl-C", took)
	}
	if fake.Agent() != nil {
		t.Error("the pairing agent was left registered")
	}
}
