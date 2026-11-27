package bluez

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// BlueZ keeps unpaired devices it has seen recently (30 s by default, or
// longer while another tool is scanning). Such a device is still a scan
// result once it is heard again; only paired devices are excluded.
func TestScanReportsCachedDevicesThatAdvertise(t *testing.T) {
	fake, client := newTestClient(t)
	cached := fake.AddDevice(fakebluez.Device{Address: "CC:CC:CC:CC:CC:01", Name: "Cached Speaker", Battery: -1})
	fake.AddDevice(fakebluez.Device{Address: "CC:CC:CC:CC:CC:02", Name: "Cached But Gone", Battery: -1})
	paired := fake.AddDevice(fakebluez.Device{Address: "CC:CC:CC:CC:CC:03", Name: "Paired Mouse", Paired: true, Battery: -1})
	fake.OnStartDiscovery(func(b *fakebluez.BlueZ) {
		time.Sleep(50 * time.Millisecond)
		b.SetProperty(cached, fakebluez.DeviceIface, "RSSI", int16(-60))
		b.SetProperty(paired, fakebluez.DeviceIface, "RSSI", int16(-50))
		b.AddDevice(fakebluez.Device{Address: "CC:CC:CC:CC:CC:04", Name: "Brand New", RSSI: -70, Battery: -1})
		b.AddDevice(fakebluez.Device{Adapter: "hci1", Address: "CC:CC:CC:CC:CC:05", Name: "Other Adapter", Battery: -1})
	})

	var mu sync.Mutex
	var events []string
	devices, err := client.Scan(testContext(t), 500*time.Millisecond, func(ev ScanEvent) {
		mu.Lock()
		events = append(events, ev.Device.Name)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var names []string
	for _, d := range devices {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	if want := []string{"Brand New", "Cached Speaker"}; !equalStrings(names, want) {
		t.Errorf("scan results = %v, want %v", names, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Errorf("scan events = %v, want one per result", events)
	}
	if fake.Calls("Adapter1.StopDiscovery") != 1 {
		t.Error("discovery was not stopped")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
