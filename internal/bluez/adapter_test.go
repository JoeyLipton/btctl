package bluez

import (
	"errors"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// twoAdapterFake returns a fake with hci0 and hci1, one paired device on each.
func twoAdapterFake(t *testing.T) *fakebluez.BlueZ {
	t.Helper()
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:00:00:00:00:00", true)
	fake.AddAdapter("hci1", "11:11:11:11:11:11", true)
	fake.AddDevice(fakebluez.Device{Adapter: "hci0", Address: "AA:AA:AA:AA:AA:00", Name: "OnHci0", Paired: true, Battery: -1})
	fake.AddDevice(fakebluez.Device{Adapter: "hci1", Address: "AA:AA:AA:AA:AA:11", Name: "OnHci1", Paired: true, Battery: -1})
	return fake
}

// newClientWith connects a client with opts and performs a first BlueZ call,
// which is where adapter selection is resolved (and can fail).
func newClientWith(t *testing.T, opts Options) (*Client, error) {
	t.Helper()
	client, err := New(opts)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.ListAdapters(testContext(t)); err != nil {
		return nil, err
	}
	return client, nil
}

func deviceNames(t *testing.T, client *Client) []string {
	t.Helper()
	devices, err := client.ListDevices(testContext(t))
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	var names []string
	for _, d := range devices {
		names = append(names, d.Name)
	}
	return names
}

func TestDefaultsToFirstAdapter(t *testing.T) {
	twoAdapterFake(t)
	client, err := newClientWith(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.ActiveAdapter(); got != fakebluez.AdapterPath("hci0") {
		t.Errorf("ActiveAdapter = %s, want hci0", got)
	}
	if names := deviceNames(t, client); len(names) != 1 || names[0] != "OnHci0" {
		t.Errorf("devices = %v, want [OnHci0]", names)
	}
}

func TestPreferredAdapterIsUsed(t *testing.T) {
	twoAdapterFake(t)
	client, err := newClientWith(t, Options{Adapter: "hci1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.ActiveAdapter(); got != fakebluez.AdapterPath("hci1") {
		t.Errorf("ActiveAdapter = %s, want hci1", got)
	}
	if names := deviceNames(t, client); len(names) != 1 || names[0] != "OnHci1" {
		t.Errorf("devices = %v, want [OnHci1]", names)
	}
	info, err := client.ActiveAdapterInfo(testContext(t))
	if err != nil || info.Name != "hci1" || info.Address != "11:11:11:11:11:11" {
		t.Errorf("ActiveAdapterInfo = %+v, %v; want hci1", info, err)
	}
}

func TestMissingRequiredAdapterFails(t *testing.T) {
	twoAdapterFake(t)
	if _, err := newClientWith(t, Options{Adapter: "hci7"}); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("New with a missing -a adapter = %v, want ErrAdapterNotFound", err)
	}
}

func TestMissingOptionalAdapterFallsBack(t *testing.T) {
	twoAdapterFake(t)
	client, err := newClientWith(t, Options{Adapter: "hci7", AdapterOptional: true})
	if err != nil {
		t.Fatalf("New with a missing remembered adapter: %v", err)
	}
	if got := client.ActiveAdapter(); got != fakebluez.AdapterPath("hci0") {
		t.Errorf("ActiveAdapter = %s, want fallback to hci0", got)
	}
}

func TestSelectAdapterSwitchesTarget(t *testing.T) {
	twoAdapterFake(t)
	client, err := newClientWith(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	adapters, err := client.ListAdapters(testContext(t))
	if err != nil || len(adapters) != 2 {
		t.Fatalf("ListAdapters = %v, %v", adapters, err)
	}
	client.SelectAdapter(adapters[1])
	if names := deviceNames(t, client); len(names) != 1 || names[0] != "OnHci1" {
		t.Errorf("devices after SelectAdapter(hci1) = %v, want [OnHci1]", names)
	}
}
