package bluez

import (
	"errors"
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

	if _, err := client.Connect(testContext(t), path, nil); err != nil {
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
	if _, err := client.Connect(testContext(t), path, nil); err == nil {
		t.Fatal("Connect succeeded, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Connect took %s, want it to give up after ~300ms", elapsed)
	}
}

// Connect pairs a device that is not paired yet before connecting it, and
// only consults the agent about that device.
func TestConnectPairsNewDevice(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Name: "New Speaker", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "RequestConfirmation", Passkey: 99})
	client.timeouts.servicesResolved = 0
	agent := NewAgent(&recordingHandler{})
	if err := client.RegisterAgent(testContext(t), agent); err != nil {
		t.Fatal(err)
	}

	var stages []ConnectStage
	result, err := client.Connect(testContext(t), path, func(s ConnectStage) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !result.Paired || result.BondingPending {
		t.Errorf("result = %+v, want Paired and not BondingPending", result)
	}
	if len(stages) != 2 || stages[0] != StagePairing || stages[1] != StageConnecting {
		t.Errorf("stages = %v, want [pairing connecting]", stages)
	}
	for _, prop := range []string{"Paired", "Trusted", "Connected"} {
		if v, _ := fake.Property(path, fakebluez.DeviceIface, prop).(bool); !v {
			t.Errorf("%s = false after Connect", prop)
		}
	}
}

func TestFindDeviceSeesUnpairedDevices(t *testing.T) {
	fake, client := newTestClient(t)
	fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Name: "Cached", Battery: -1})
	d, err := client.FindDevice(testContext(t), "aa:aa:aa:aa:aa:02")
	if err != nil || d.Name != "Cached" || d.Paired {
		t.Fatalf("FindDevice = %+v, %v; want the unpaired Cached device", d, err)
	}
	if _, err := client.FindDevice(testContext(t), "AA:AA:AA:AA:AA:99"); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("FindDevice(unknown) = %v, want ErrDeviceNotFound", err)
	}
}
