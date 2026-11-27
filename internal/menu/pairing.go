// Package menu - pairing.go is the menu's connect path and its pairing
// handler. Pairing prompts are not driven from here: while Pair/Connect are
// in flight BlueZ calls the agent, which asks pairingHandler, which shows a
// picker.
package menu

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joeylipton/btctl/internal/bluez"
)

// connect pairs (when needed) and connects d, reporting the outcome. Failures
// are reported, not returned: they must not end the menu session.
func (m *Menu) connect(ctx context.Context, d bluez.Device) {
	name := d.DisplayName()
	result, err := m.client.Connect(ctx, d.Path, func(stage bluez.ConnectStage) {
		switch stage {
		case bluez.StagePairing:
			m.progressf("pairing with %s (%s)...", name, d.Address)
		case bluez.StageConnecting:
			m.progressf("connecting to %s (%s)...", name, d.Address)
		}
	})
	switch {
	case errors.Is(err, bluez.ErrConnectInProgress):
		m.tell("Already connecting to %s", name)
	case err != nil:
		m.tell("Could not connect to %s: %s", name, bluez.FormatUserError(err))
	case result.BondingPending:
		m.tell("Connected to %s (bonding may still be completing)", name)
	default:
		m.tell("Connected to %s", name)
	}
}

// PairingHandler returns the handler that answers BlueZ pairing requests with
// pickers. Register an agent built from it while the menu runs.
func (m *Menu) PairingHandler() bluez.PairingHandler { return pairingHandler{m} }

type pairingHandler struct{ m *Menu }

// Confirm shows a yes/no picker. Cancelling the request (BlueZ calling
// Cancel) closes the picker.
func (h pairingHandler) Confirm(ctx context.Context, req bluez.PairingRequest) bool {
	name := h.m.client.DeviceName(ctx, req.Device)
	h.m.alert("btctl - pairing request", "Pair with "+name+"? Answer in the menu.")

	var lines []string
	if req.Kind == bluez.PairingConfirm {
		lines = append(lines, fmt.Sprintf("Passkey: %06d (must match the device)", req.Passkey))
	}
	lines = append(lines, "yes - confirm pairing", "no  - reject pairing")
	choice, err := h.m.prompt(ctx, "Pairing request - "+name, lines)
	if err != nil {
		h.m.tell("Could not show the pairing prompt: %s", err)
		return false
	}
	return strings.HasPrefix(choice, "yes")
}

// Display raises a notification with the code the user must type on the
// device; the picker cannot show it while BlueZ waits for the keystrokes.
func (h pairingHandler) Display(req bluez.PairingRequest) {
	if req.Entered > 0 {
		return // BlueZ repeats DisplayPasskey for every key typed
	}
	ctx, cancel := context.WithTimeout(context.Background(), nameLookupTimeout)
	defer cancel()
	msg := fmt.Sprintf("Type %s on %s, then press Enter", req.Code(), h.m.client.DeviceName(ctx, req.Device))
	h.m.progressf("%s", msg)
	h.m.alert("btctl - pairing code", msg)
}

// Input asks for a passkey or PIN with a free-text picker. For PINs the
// conventional 0000 is offered as a ready-made choice.
func (h pairingHandler) Input(ctx context.Context, req bluez.PairingRequest) (string, bool) {
	name := h.m.client.DeviceName(ctx, req.Device)
	title := "Enter the passkey shown on " + name
	var lines []string
	if req.Kind == bluez.PairingEnterPinCode {
		title = "Enter the PIN for " + name
		lines = []string{bluez.DefaultPinCode}
	}
	h.m.alert("btctl - pairing request", title)
	value, err := h.m.prompt(ctx, title, lines)
	if err != nil || value == "" {
		return "", false
	}
	return value, true
}
