package bluetooth

import (
	"testing"
	"time"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// Device1.Connect only replies once the link is up. A connect that takes
// longer than the ordinary per-call timeout must still succeed.
func TestConnectOutlivesPerCallTimeout(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Headphones", Paired: true, Bonded: true, Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{ConnectDelay: 600 * time.Millisecond})
	client.timeouts.call = 200 * time.Millisecond

	if err := client.Connect(testContext(t), path); err != nil {
		t.Fatalf("Connect with a 600ms Device1.Connect and 200ms call timeout: %v", err)
	}
	if connected, _ := fake.Property(path, fakebluez.DeviceIface, "Connected").(bool); !connected {
		t.Error("device is not connected after Connect returned")
	}
}

// The connect timeout still applies: a Device1.Connect that never finishes
// must fail instead of hanging.
func TestConnectHonoursConnectTimeout(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Headphones", Paired: true, Bonded: true, Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{ConnectDelay: 3 * time.Second})
	client.timeouts.connect = 300 * time.Millisecond

	start := time.Now()
	if err := client.Connect(testContext(t), path); err == nil {
		t.Fatal("Connect succeeded, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Connect took %s, want it to give up after ~300ms", elapsed)
	}
}
