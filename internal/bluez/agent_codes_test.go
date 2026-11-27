package bluez

import (
	"context"
	"sync"
	"testing"
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
