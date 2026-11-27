package bluetooth

import (
	"context"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/joeylipton/btctl/internal/fakebluez"
)

// "/org/bluez/hci1" is a string prefix of "/org/bluez/hci10/...".
func TestListDevicesDoesNotMixUpHci1AndHci10(t *testing.T) {
	fake := fakebluez.New(t)
	fake.AddAdapter("hci1", "11:11:11:11:11:11", true)
	fake.AddAdapter("hci10", "10:10:10:10:10:10", true)
	fake.AddDevice(fakebluez.Device{Adapter: "hci1", Address: "AA:AA:AA:AA:AA:01", Name: "OnHci1", Paired: true, Battery: -1})
	fake.AddDevice(fakebluez.Device{Adapter: "hci10", Address: "AA:AA:AA:AA:AA:10", Name: "OnHci10", Paired: true, Battery: -1})
	client, err := newClientWith(t, Options{Adapter: "hci1"})
	if err != nil {
		t.Fatal(err)
	}
	if names := deviceNames(t, client); len(names) != 1 || names[0] != "OnHci1" {
		t.Errorf("devices on hci1 = %v, want [OnHci1]", names)
	}
}

func TestWatchPropertyReturnsAtOnceWhenAlreadySet(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Paired: true, Connected: true, Battery: -1})
	fake.ResetCalls()

	start := time.Now()
	if err := client.watchProperty(testContext(t), path, bluezDeviceIface, propConnected, boolPropertyWant(true)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("watch of an already-true property took %s", elapsed)
	}
	if n := fake.Calls("Properties.Get"); n != 1 {
		t.Errorf("Properties.Get called %d times, want 1", n)
	}
}

// The watch must be driven by PropertiesChanged signals, not by polling
// Properties.Get every 100 ms.
func TestWatchPropertyUsesSignals(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Paired: true, Battery: -1})
	fake.ResetCalls()
	go func() {
		time.Sleep(time.Second)
		fake.SetProperty(path, fakebluez.DeviceIface, "Connected", true)
	}()

	start := time.Now()
	if err := client.watchProperty(testContext(t), path, bluezDeviceIface, propConnected, boolPropertyWant(true)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("watch noticed the change after %s", elapsed)
	}
	if n := fake.Calls("Properties.Get"); n > 3 {
		t.Errorf("Properties.Get called %d times during a 1 s wait, want at most 3", n)
	}
}

func TestWatchPropertyTimesOut(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Paired: true, Battery: -1})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := client.watchProperty(ctx, path, bluezDeviceIface, propConnected, boolPropertyWant(true)); err == nil {
		t.Fatal("watch of a property that never changes succeeded")
	}
}

// PairDevice must not leave its Paired watcher polling after Pair() fails.
func TestPairDeviceFailureStopsWatching(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{PairErr: dbus.NewError(errNameAuthFailed, []any{"Authentication Failed"})})

	if err := client.PairDevice(testContext(t), path); !isDBusError(err, errNameAuthFailed) {
		t.Fatalf("PairDevice = %v, want AuthenticationFailed", err)
	}
	fake.ResetCalls()
	time.Sleep(1500 * time.Millisecond)
	if n := fake.Calls("Properties.Get"); n != 0 {
		t.Errorf("%d Properties.Get calls after PairDevice returned; its watcher is still running", n)
	}
}
