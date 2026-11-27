package bluez

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/joeylipton/btctl/internal/fakebluez"
)

// countingHandler accepts everything and counts how often it was consulted.
type countingHandler struct{ calls atomic.Int32 }

func (h *countingHandler) Confirm(context.Context, PairingRequest) bool { h.calls.Add(1); return true }
func (h *countingHandler) Display(PairingRequest)                       { h.calls.Add(1) }
func (h *countingHandler) Input(context.Context, PairingRequest) (string, bool) {
	h.calls.Add(1)
	return "123456", true
}

const strangerDevice = dbus.ObjectPath("/org/bluez/hci0/dev_66_66_66_66_66_66")

// btctl is the default agent while it runs; a nearby device that starts
// pairing on its own must be turned away without asking anyone.
func TestAgentRejectsUnexpectedDevices(t *testing.T) {
	fake, client := newTestClient(t)
	handler := &countingHandler{}
	registerTestAgent(t, client, handler) // expects testDevice only

	ctx := testContext(t)
	requests := []struct {
		method string
		args   []any
	}{
		{"RequestConfirmation", []any{uint32(1)}},
		{"RequestAuthorization", nil},
		{"RequestPasskey", nil},
		{"RequestPinCode", nil},
		{"DisplayPasskey", []any{uint32(1), uint16(0)}},
		{"DisplayPinCode", []any{"1234"}},
		{"AuthorizeService", []any{"0000110b-0000-1000-8000-00805f9b34fb"}},
	}
	for _, r := range requests {
		if _, err := fake.CallAgent(ctx, r.method, strangerDevice, r.args...); !isDBusError(err, errNameRejected) {
			t.Errorf("%s from an unexpected device = %v, want Rejected", r.method, err)
		}
	}
	if n := handler.calls.Load(); n != 0 {
		t.Errorf("handler consulted %d times for unexpected devices, want 0", n)
	}
	if _, err := fake.CallAgent(ctx, "AuthorizeService", testDevice, "0000110b-0000-1000-8000-00805f9b34fb"); err != nil {
		t.Errorf("AuthorizeService for the device being connected = %v, want accepted", err)
	}
}

// Pairing a device only authorises that device, and only while pairing.
func TestPairingAuthorisesOnlyTheTarget(t *testing.T) {
	fake, client := newTestClient(t)
	target := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:02", Name: "Target", Battery: -1})
	fake.SetBehavior(target, fakebluez.Behavior{AgentRequest: "RequestConfirmation", Passkey: 1})
	client.timeouts.servicesResolved = 0

	var strangerErr atomic.Value
	handler := funcHandler(func(ctx context.Context, req PairingRequest) bool {
		// While the target is being paired, a stranger tries its luck.
		_, err := fake.CallAgent(ctx, "RequestAuthorization", strangerDevice)
		strangerErr.Store(err)
		return req.Device == target
	})
	agent := NewAgent(handler)
	if err := client.RegisterAgent(testContext(t), agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.UnregisterAgent(context.Background(), agent) })

	if err := client.Pair(testContext(t), target); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if err, _ := strangerErr.Load().(error); !isDBusError(err, errNameRejected) {
		t.Errorf("stranger during pairing = %v, want Rejected", err)
	}
	if _, err := fake.CallAgent(testContext(t), "RequestAuthorization", target); !isDBusError(err, errNameRejected) {
		t.Errorf("target after pairing finished = %v, want Rejected", err)
	}
}
