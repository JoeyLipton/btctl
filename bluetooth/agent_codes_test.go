package bluetooth

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// recordingHandler accepts confirmations, records displayed codes and
// answers input requests with a fixed value.
type recordingHandler struct {
	mu        sync.Mutex
	displayed []PairingRequest
	input     string
}

func (h *recordingHandler) Confirm(context.Context, PairingRequest) bool { return true }
func (h *recordingHandler) Display(req PairingRequest) {
	h.mu.Lock()
	h.displayed = append(h.displayed, req)
	h.mu.Unlock()
}
func (h *recordingHandler) Input(context.Context, PairingRequest) (string, bool) {
	return h.input, h.input != ""
}

func (h *recordingHandler) shown() []PairingRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PairingRequest(nil), h.displayed...)
}

// A keyboard pairs with passkey entry: btctl must show the code to type.
func TestDisplayPasskeyReachesHandler(t *testing.T) {
	fake, client := newTestClient(t)
	handler := &recordingHandler{}
	registerTestAgent(t, client, handler)

	if _, err := fake.CallAgent(testContext(t), "DisplayPasskey", testDevice, uint32(12345), uint16(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.CallAgent(testContext(t), "DisplayPinCode", testDevice, "8264"); err != nil {
		t.Fatal(err)
	}
	displayed := handler.shown()
	if len(displayed) != 2 {
		t.Fatalf("handler displayed %d codes, want 2", len(displayed))
	}
	if got := displayed[0].Code(); got != "012345" {
		t.Errorf("passkey shown as %q, want 012345", got)
	}
	if got := displayed[1].Code(); got != "8264" {
		t.Errorf("PIN shown as %q, want 8264", got)
	}
}

func TestPasskeyAndPinEntry(t *testing.T) {
	tests := []struct {
		method, input string
		want          any
		wantErr       string
	}{
		{"RequestPasskey", "123456", uint32(123456), ""},
		{"RequestPasskey", " 042 ", uint32(42), ""},
		{"RequestPasskey", "1234567", nil, errNameRejected},
		{"RequestPasskey", "12ab", nil, errNameRejected},
		{"RequestPasskey", "", nil, errNameRejected},
		{"RequestPinCode", "0000", "0000", ""},
		{"RequestPinCode", "has space", nil, errNameRejected},
	}
	for _, tt := range tests {
		t.Run(tt.method+"/"+tt.input, func(t *testing.T) {
			fake, client := newTestClient(t)
			registerTestAgent(t, client, &recordingHandler{input: tt.input})
			got, err := fake.CallAgent(testContext(t), tt.method, testDevice)
			if tt.wantErr != "" {
				if !isDBusError(err, tt.wantErr) {
					t.Fatalf("%s(%q) = %v, %v; want %s", tt.method, tt.input, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("%s(%q) = %#v, %v; want %#v", tt.method, tt.input, got, err, tt.want)
			}
		})
	}
}

// End to end: pairing a device that asks for its passkey, typed into the menu.
func TestMenuPasskeyEntryPairsDevice(t *testing.T) {
	fake, client := newTestClient(t)
	path := fake.AddDevice(fakebluez.Device{Address: "AA:AA:AA:AA:AA:01", Name: "Keyboard", Battery: -1})
	fake.SetBehavior(path, fakebluez.Behavior{AgentRequest: "RequestPasskey", Passkey: 654321})
	client.timeouts.servicesResolved = 0
	shown := fakePicker(t, "TYPE:654321")
	menu := NewMenu(client, PickerConfig{Backend: BackendRofi})
	registerTestAgent(t, client, menu.PairingHandler())

	if err := client.PairDevice(testContext(t), path); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	if !strings.Contains(shown(), "Enter the passkey shown on Keyboard") {
		t.Errorf("picker did not ask for the passkey:\n%s", shown())
	}
}

func TestCLIHandler(t *testing.T) {
	ctx := testContext(t)
	req := PairingRequest{Address: "AA:AA:AA:AA:AA:01"}

	var out bytes.Buffer
	h := CLIHandler{Out: &out}
	req.Kind, req.Passkey = PairingDisplayPasskey, 7
	h.Display(req)
	req.Entered = 3
	h.Display(req) // repeats while typing must not print again
	if got := strings.Count(out.String(), "Type 000007 on AA:AA:AA:AA:AA:01"); got != 1 {
		t.Errorf("Display printed the code %d times:\n%s", got, out.String())
	}

	req = PairingRequest{Address: "AA:AA:AA:AA:AA:01", Kind: PairingEnterPinCode}
	if v, ok := h.Input(ctx, req); !ok || v != "0000" {
		t.Errorf("non-interactive PIN = %q, %v; want 0000", v, ok)
	}
	req.Kind = PairingEnterPasskey
	if _, ok := h.Input(ctx, req); ok {
		t.Error("non-interactive passkey entry succeeded; there is nobody to type it")
	}

	h = CLIHandler{Out: &out, In: strings.NewReader("123456\n"), Interactive: true}
	if v, ok := h.Input(ctx, req); !ok || v != "123456" {
		t.Errorf("interactive passkey = %q, %v; want 123456", v, ok)
	}
	if !h.Confirm(ctx, PairingRequest{Kind: PairingConfirm, Passkey: 1}) {
		t.Error("CLIHandler rejected a confirmation")
	}
}
