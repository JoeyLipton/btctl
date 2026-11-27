package execpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindUsesPATH(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "btctl-test-prog")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got, err := Find("btctl-test-prog"); err != nil || got != prog {
		t.Errorf("Find = %q, %v; want %q", got, err, prog)
	}
}

func TestFindFallsBackToStandardDirs(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // stripped PATH, as from an i3 keybinding
	got, err := Find("sh")
	if err != nil {
		t.Fatalf("Find(sh) with a stripped PATH: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("Find(sh) = %q, want an absolute path", got)
	}
}

func TestFindMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Find("btctl-no-such-program"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(missing) = %v, want ErrNotFound", err)
	}
}
