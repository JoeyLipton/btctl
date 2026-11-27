// Package fakebluez runs a scriptable stand-in for bluetoothd on a private
// dbus-daemon so btctl's D-Bus code can be tested without Bluetooth hardware
// or access to the real system bus.
//
// New starts the bus, claims the org.bluez name on it and points
// DBUS_SYSTEM_BUS_ADDRESS at it for the duration of the test, so code that
// connects to "the system bus" talks to the fake instead.
//
// Tests are skipped when dbus-daemon is not installed, unless
// BTCTL_REQUIRE_DBUS is set (CI sets it so the tests can never silently skip).
package fakebluez

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:abstract=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

var busCounter atomic.Int64

// StartBus launches a private dbus-daemon and returns its address. The daemon
// is stopped when the test finishes.
func StartBus(tb testing.TB) string {
	tb.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("BTCTL_REQUIRE_DBUS") != "" {
			tb.Fatalf("dbus-daemon is required (BTCTL_REQUIRE_DBUS is set): %v", err)
		}
		tb.Skip("dbus-daemon not installed; skipping D-Bus integration test")
	}

	dir := tb.TempDir()
	name := fmt.Sprintf("btctl-test-%d-%d", os.Getpid(), busCounter.Add(1))
	cfgPath := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(busConfig, name)), 0o600); err != nil {
		tb.Fatalf("write bus config: %v", err)
	}

	cmd := exec.Command(daemon, "--config-file="+cfgPath, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatalf("dbus-daemon stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		tb.Fatalf("start dbus-daemon: %v", err)
	}
	tb.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	addrCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		addrCh <- strings.TrimSpace(line)
	}()
	select {
	case addr := <-addrCh:
		if addr == "" {
			tb.Fatal("dbus-daemon exited without printing its address")
		}
		return addr
	case <-time.After(10 * time.Second):
		tb.Fatal("timed out waiting for dbus-daemon to start")
		return ""
	}
}
