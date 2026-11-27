package bluetooth

import (
	"context"
	"testing"
	"time"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// newTestClient starts a fake bluetoothd with one powered adapter (hci0) and
// returns it together with a Client connected to it.
func newTestClient(t *testing.T) (*fakebluez.BlueZ, *Client) {
	t.Helper()
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:11:22:33:44:55", true)
	return fake, connectTestClient(t)
}

// connectTestClient connects a Client to the fake bus configured by
// fakebluez.New. Use it after adding adapters to the fake.
func connectTestClient(t *testing.T) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestListDevicesReturnsOnlyPairedDevices(t *testing.T) {
	fake, client := newTestClient(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Headphones", Paired: true, Connected: true, Battery: 80})
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Name: "Keyboard", Paired: true, Battery: -1})
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:03", Name: "Stranger", Battery: -1})

	devices, err := client.ListDevices(testContext(t))
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devices), devices)
	}
	if devices[0].Name != "Headphones" || !devices[0].Connected || devices[0].Battery != 80 {
		t.Errorf("first device = %+v, want connected Headphones at 80%%", devices[0])
	}
	if devices[1].Name != "Keyboard" || devices[1].Battery != batteryUnavailable {
		t.Errorf("second device = %+v, want Keyboard without battery", devices[1])
	}
}
