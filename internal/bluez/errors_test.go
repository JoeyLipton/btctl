package bluez

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/joeylipton/btctl/internal/fakebluez"
)

// bluezErr builds a D-Bus error the way godbus delivers BlueZ replies: a
// dbus.Error value whose Error() returns the message, not the name.
func bluezErr(name, msg string) error {
	return fmt.Errorf("dbus call org.bluez.Device1.Connect on /org/bluez/hci0/dev_AA: %w",
		dbus.Error{Name: name, Body: []any{msg}})
}

func TestFormatUserError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"not ready", bluezErr(errNameNotReady, "Resource Not Ready"), "adapter not ready (is it powered on?)"},
		{"failed keeps reason", bluezErr(errNameFailed, "br-connection-page-timeout"), "connection failed: br-connection-page-timeout"},
		{"failed without reason", bluezErr(errNameFailed, "Failed"), "connection failed"},
		{"bluez not running", fmt.Errorf("probe: %w", dbus.Error{
			Name: errNameServiceUnknown,
			Body: []any{"The name org.bluez was not provided by any .service files"},
		}), "BlueZ is not running"},
		{"pointer form", fmt.Errorf("x: %w", dbus.NewError(errNameAuthFailed, []any{"Authentication Failed"})), "authentication failed"},
		{"unknown dbus error passes through", bluezErr("org.example.Weird", "weird"), "dbus call org.bluez.Device1.Connect on /org/bluez/hci0/dev_AA: weird"},
		{"timeout", fmt.Errorf("connect: %w", context.DeadlineExceeded), "timed out waiting for BlueZ"},
		{"sentinel passes through", fmt.Errorf("lookup device X: %w", ErrDeviceNotFound), "lookup device X: device not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatUserError(tt.err); got != tt.want {
				t.Errorf("FormatUserError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorClassifiersUseNames(t *testing.T) {
	if !isDBusError(bluezErr(errNameNotConnected, "Not Connected"), errNameNotConnected) {
		t.Error("isDBusError did not match NotConnected")
	}
	if isDBusError(errors.New("org.bluez.Error.NotConnected"), errNameNotConnected) {
		t.Error("isDBusError matched a plain error by its text")
	}
	if !propertyAbsent(dbus.Error{Name: errNameInvalidArgs, Body: []any{"No such property 'Bonded'"}}) {
		t.Error("propertyAbsent did not recognise InvalidArgs")
	}
	for _, err := range []error{
		bluezErr(errNameBlocked, "Blocked through rfkill"),
		bluezErr(errNameFailed, "Blocked through rfkill"),
		bluezErr(errNameNotReady, "Resource Not Ready"),
	} {
		if !adapterPoweredNeedsRecovery(err) {
			t.Errorf("adapterPoweredNeedsRecovery(%v) = false, want true", err)
		}
	}
	if adapterPoweredNeedsRecovery(bluezErr(errNameFailed, "Busy")) {
		t.Error("adapterPoweredNeedsRecovery accepted an unrelated failure")
	}
}

func TestDisconnectAlreadyDisconnectedSucceeds(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Speaker", Paired: true, Battery: -1})

	if err := client.Disconnect(testContext(t), path); err != nil {
		t.Fatalf("Disconnect on an already-disconnected device = %v, want nil", err)
	}
	if got := fake.Calls("Device1.Disconnect"); got != 1 {
		t.Errorf("Device1.Disconnect called %d times, want 1", got)
	}
}
