package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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

var outputMu sync.Mutex

// runCLI runs btctl in-process with args and returns its exit code and the
// text it wrote to stdout and stderr.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	outputMu.Lock()
	defer outputMu.Unlock()

	capture := func(target **os.File) (restore func() string) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := *target
		*target = w
		var buf bytes.Buffer
		done := make(chan struct{})
		go func() { _, _ = io.Copy(&buf, r); close(done) }()
		return func() string {
			*target = orig
			_ = w.Close()
			<-done
			return buf.String()
		}
	}
	restoreOut := capture(&os.Stdout)
	restoreErr := capture(&os.Stderr)
	code = runMain(args)
	return code, restoreOut(), restoreErr()
}

func TestParseGlobalFlags(t *testing.T) {
	tests := []struct {
		args        []string
		wantVerbose bool
		wantAdapter string
		wantRest    string
		wantErr     bool
	}{
		{args: []string{"list"}, wantRest: "list"},
		{args: []string{"-v", "list"}, wantVerbose: true, wantRest: "list"},
		{args: []string{"-a", "hci1", "scan"}, wantAdapter: "hci1", wantRest: "scan"},
		{args: []string{"--adapter=hci2", "-v", "status"}, wantVerbose: true, wantAdapter: "hci2", wantRest: "status"},
		{args: []string{"-a"}, wantErr: true},
		{args: []string{"-a", "eth0", "list"}, wantErr: true},
	}
	for _, tt := range tests {
		opts, rest, err := parseGlobalFlags(tt.args)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseGlobalFlags(%q) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if opts.verbose != tt.wantVerbose || opts.adapter != tt.wantAdapter || strings.Join(rest, " ") != tt.wantRest {
			t.Errorf("parseGlobalFlags(%q) = %+v %q, want verbose=%v adapter=%q rest=%q",
				tt.args, opts, rest, tt.wantVerbose, tt.wantAdapter, tt.wantRest)
		}
	}
}
