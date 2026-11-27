package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/joeylipton/btctl/internal/bluez"
)

func TestCLIPairing(t *testing.T) {
	ctx := context.Background()
	req := bluez.PairingRequest{Address: "AA:AA:AA:AA:AA:01"}

	var out bytes.Buffer
	h := cliPairing{Out: &out}
	req.Kind, req.Passkey = bluez.PairingDisplayPasskey, 7
	h.Display(req)
	req.Entered = 3
	h.Display(req) // repeats while typing must not print again
	if got := strings.Count(out.String(), "Type 000007 on AA:AA:AA:AA:AA:01"); got != 1 {
		t.Errorf("Display printed the code %d times:\n%s", got, out.String())
	}

	req = bluez.PairingRequest{Address: "AA:AA:AA:AA:AA:01", Kind: bluez.PairingEnterPinCode}
	if v, ok := h.Input(ctx, req); !ok || v != "0000" {
		t.Errorf("non-interactive PIN = %q, %v; want 0000", v, ok)
	}
	req.Kind = bluez.PairingEnterPasskey
	if _, ok := h.Input(ctx, req); ok {
		t.Error("non-interactive passkey entry succeeded; there is nobody to type it")
	}

	h = cliPairing{Out: &out, In: strings.NewReader("123456\n"), Interactive: true}
	if v, ok := h.Input(ctx, req); !ok || v != "123456" {
		t.Errorf("interactive passkey = %q, %v; want 123456", v, ok)
	}
	if !h.Confirm(ctx, bluez.PairingRequest{Kind: bluez.PairingConfirm, Passkey: 1}) {
		t.Error("cliPairing rejected a confirmation")
	}
}
