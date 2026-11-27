package bluetooth

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/joeylipton/btctl/internal/fakebluez"
)

// funcHandler adapts a function to PairingHandler; it never displays codes
// and rejects input requests.
type funcHandler func(ctx context.Context, req PairingRequest) bool

func (f funcHandler) Confirm(ctx context.Context, req PairingRequest) bool { return f(ctx, req) }
func (f funcHandler) Display(PairingRequest)                               {}
func (f funcHandler) Input(context.Context, PairingRequest) (string, bool) { return "", false }

// registerTestAgent registers an agent backed by handler on client and lets
// it accept requests for testDevice, as if btctl were pairing it.
func registerTestAgent(t *testing.T, client *Client, handler PairingHandler) *Agent {
	t.Helper()
	agent := NewAgent(handler)
	if err := client.RegisterAgent(testContext(t), agent); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	t.Cleanup(agent.Expect(testDevice))
	t.Cleanup(func() { _ = client.UnregisterAgent(context.Background(), agent) })
	return agent
}

const testDevice = dbus.ObjectPath("/org/bluez/hci0/dev_AA_AA_AA_AA_AA_01")

// The old agent answered through a non-blocking send on an unbuffered channel
// and lost answers whenever the D-Bus goroutine was not parked yet, most
// reliably with GOMAXPROCS=1. Every request must now be answered.
func TestAgentAnswersEveryRequest(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	fake, client := newTestClient(t)
	var calls atomic.Int32
	registerTestAgent(t, client, funcHandler(func(_ context.Context, req PairingRequest) bool {
		calls.Add(1)
		return req.Passkey == 123456 && req.Address == "AA:AA:AA:AA:AA:01"
	}))

	ctx := testContext(t)
	for i := 0; i < 50; i++ {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := fake.CallAgent(callCtx, "RequestConfirmation", testDevice, uint32(123456))
		cancel()
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 50 {
		t.Errorf("handler called %d times, want 50", got)
	}
}

func TestAgentRejectsWhenHandlerDeclines(t *testing.T) {
	fake, client := newTestClient(t)
	registerTestAgent(t, client, funcHandler(func(context.Context, PairingRequest) bool { return false }))

	_, err := fake.CallAgent(testContext(t), "RequestAuthorization", testDevice)
	if !isDBusError(err, errNameRejected) {
		t.Fatalf("RequestAuthorization = %v, want org.bluez.Error.Rejected", err)
	}
}

// BlueZ's Cancel used to permanently stop the auto-confirm loop (and the
// menu's relay); later requests then hung for 60 s. Cancel must only abort
// the request in flight.
func TestAgentSurvivesCancel(t *testing.T) {
	fake, client := newTestClient(t)
	var accept atomic.Bool
	registerTestAgent(t, client, funcHandler(func(ctx context.Context, _ PairingRequest) bool {
		if accept.Load() {
			return true
		}
		<-ctx.Done() // wait for the user... who never answers
		return false
	}))

	ctx := testContext(t)
	errCh := make(chan error, 1)
	go func() {
		_, err := fake.CallAgent(ctx, "RequestConfirmation", testDevice, uint32(1))
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if err := fake.CancelAgent(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-errCh:
		if !isDBusError(err, errNameCanceled) {
			t.Fatalf("cancelled request = %v, want org.bluez.Error.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not abort the pending request")
	}

	accept.Store(true)
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := fake.CallAgent(callCtx, "RequestConfirmation", testDevice, uint32(2)); err != nil {
		t.Fatalf("request after Cancel = %v, want accepted", err)
	}
}

func TestPairDeviceConsultsAgent(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Phone", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "RequestConfirmation", Passkey: 4242})
	client.timeouts.servicesResolved = 200 * time.Millisecond
	var seen atomic.Uint32
	registerTestAgent(t, client, funcHandler(func(_ context.Context, req PairingRequest) bool {
		seen.Store(req.Passkey)
		return true
	}))

	if err := client.PairDevice(testContext(t), path); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	if seen.Load() != 4242 {
		t.Errorf("handler saw passkey %d, want 4242", seen.Load())
	}
	if paired, _ := fake.Property(path, fakebluez.DeviceIface, "Paired").(bool); !paired {
		t.Error("device not paired")
	}
}

func TestMenuPairingPromptsUser(t *testing.T) {
	for _, tt := range []struct {
		answer string
		wantOK bool
	}{
		{"^yes", true},
		{"^no", false},
	} {
		t.Run(tt.answer, func(t *testing.T) {
			fake, client := newTestClient(t)
			fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Phone", Battery: -1})
			shown := fakePicker(t, tt.answer)
			menu := NewMenu(client, PickerConfig{Backend: BackendRofi})
			registerTestAgent(t, client, menu.PairingHandler())

			_, err := fake.CallAgent(testContext(t), "RequestConfirmation", testDevice, uint32(7))
			if (err == nil) != tt.wantOK {
				t.Fatalf("RequestConfirmation = %v, want accepted=%v", err, tt.wantOK)
			}
			log := shown()
			for _, want := range []string{"Pairing request - Phone", "Passkey: 000007"} {
				if !strings.Contains(log, want) {
					t.Errorf("picker was not shown %q:\n%s", want, log)
				}
			}
		})
	}
}

func TestMenuPairingPromptClosesOnCancel(t *testing.T) {
	fake, client := newTestClient(t)
	fakePicker(t, "^yes")
	t.Setenv("FAKE_PICKER_SLEEP", "30")
	menu := NewMenu(client, PickerConfig{Backend: BackendRofi})
	registerTestAgent(t, client, menu.PairingHandler())

	ctx := testContext(t)
	errCh := make(chan error, 1)
	go func() {
		_, err := fake.CallAgent(ctx, "RequestConfirmation", testDevice, uint32(7))
		errCh <- err
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if err := fake.CancelAgent(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !isDBusError(err, errNameCanceled) {
			t.Errorf("request = %v, want Canceled", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("picker took %s to close after Cancel", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("picker was not closed when BlueZ cancelled the request")
	}
}
