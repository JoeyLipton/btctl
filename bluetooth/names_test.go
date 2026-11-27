package bluetooth

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/joeylipton/btctl/internal/fakebluez"
)

func TestDeviceNamePrefersAlias(t *testing.T) {
	const addr = "AA:BB:CC:DD:EE:FF"
	v := dbus.MakeVariant
	tests := []struct {
		name  string
		props map[string]dbus.Variant
		want  string
	}{
		{"renamed by the user", map[string]dbus.Variant{"Name": v("WH-1000XM4"), "Alias": v("Living room")}, "Living room"},
		{"alias mirrors name", map[string]dbus.Variant{"Name": v("WH-1000XM4"), "Alias": v("WH-1000XM4")}, "WH-1000XM4"},
		{"no name: alias is the address", map[string]dbus.Variant{"Alias": v("AA-BB-CC-DD-EE-FF")}, ""},
		{"name only", map[string]dbus.Variant{"Name": v("Mouse")}, "Mouse"},
		{"nothing", map[string]dbus.Variant{}, ""},
	}
	for _, tt := range tests {
		if got := deviceName(tt.props, addr); got != tt.want {
			t.Errorf("%s: deviceName = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestListDevicesShowsAlias(t *testing.T) {
	fake, client := newTestClient(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "WH-1000XM4", Alias: "Living room", Paired: true, Battery: -1})
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Paired: true, Battery: -1}) // no name
	names := deviceNames(t, client)
	if len(names) != 2 || names[0] != "Living room" || names[1] != "Unknown" {
		t.Errorf("names = %q, want [Living room Unknown]", names)
	}
}
