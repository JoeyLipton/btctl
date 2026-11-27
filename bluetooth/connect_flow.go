// Package bluetooth - connect_flow.go is the menu's connect path and the
// menu's pairing handler. Pairing prompts are not driven from here: while
// Pair/Connect are in flight BlueZ calls the agent, which asks menuPairing,
// which shows a yes/no picker.
package bluetooth

import (
	"context"
	"fmt"
	"strings"
)

// connectWithConfirmation pairs (when needed) and connects device, reporting
// the outcome to the user. Failures are reported, not returned: they must not
// end the menu session.
func (m *Menu) connectWithConfirmation(parentCtx context.Context, device Device) error {
	mu := m.client.connLock(device.Path)
	if !mu.TryLock() {
		menuStderr("already connecting to %s", devicePresentName(device))
		return nil
	}
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, m.client.timeouts.connection)
	defer cancel()

	name := devicePresentName(device)
	if !device.Paired {
		menuStderr("pairing with %s (%s)...", name, device.Address)
		if err := m.client.PairDevice(ctx, device.Path); err != nil {
			menuStderr("error: %s", err)
			notify(notifyAppName, "Could not pair with "+name+": "+FormatUserError(err))
			return nil
		}
	}
	menuStderr("connecting to %s (%s)...", name, device.Address)
	if err := m.client.connectDirect(ctx, device.Path); err != nil {
		menuStderr("error: %s", err) // connectDirect already notified
		return nil
	}
	menuStderr("connected to %s (%s)", name, device.Address)
	return nil
}

// PairingHandler returns the handler that answers BlueZ pairing requests with
// picker prompts. Register an Agent built from it while the menu runs.
func (m *Menu) PairingHandler() PairingHandler { return menuPairing{m} }

// menuPairing asks the user about pairing requests through the picker.
type menuPairing struct{ m *Menu }

// Confirm shows a yes/no picker for the request; cancelling the request (for
// example BlueZ calling Cancel) closes the picker.
func (p menuPairing) Confirm(ctx context.Context, req PairingRequest) bool {
	name := p.m.client.getDeviceName(ctx, req.Device)
	notifyAlert("btctl - pairing request", "Pair with "+name+"? Answer in the menu.")
	return p.m.confirmPairingPrompt(ctx, name, req)
}

// Display raises a notification with the code the user must type on the
// device; the picker cannot show it while BlueZ waits for the keystrokes.
func (p menuPairing) Display(req PairingRequest) {
	if req.Entered > 0 {
		return // BlueZ repeats DisplayPasskey for every key typed
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.m.client.timeouts.call)
	defer cancel()
	msg := fmt.Sprintf("Type %s on %s, then press Enter", req.Code(), p.m.client.getDeviceName(ctx, req.Device))
	menuStderr("%s", msg)
	notifyAlert("btctl - pairing code", msg)
}

// Input asks for a passkey or PIN with a free-text picker. For PINs the
// conventional 0000 is offered as a ready-made choice.
func (p menuPairing) Input(ctx context.Context, req PairingRequest) (string, bool) {
	name := p.m.client.getDeviceName(ctx, req.Device)
	title := "Enter the passkey shown on " + name
	var lines []string
	if req.Kind == PairingEnterPinCode {
		title = "Enter the PIN for " + name
		lines = []string{defaultPinCode}
	}
	notifyAlert("btctl - pairing request", title)
	value, err := p.m.promptContext(ctx, title, lines)
	if err != nil || value == "" {
		return "", false
	}
	return value, true
}

// confirmPairingPrompt opens the yes/no confirmation picker for an
// in-flight pairing request and returns true iff the user accepted.
func (m *Menu) confirmPairingPrompt(ctx context.Context, name string, req PairingRequest) bool {
	var lines []string
	if req.Kind == PairingConfirm {
		lines = append(lines, fmt.Sprintf("Passkey: %06d (must match the device)", req.Passkey))
	}
	// Static prompt lines, not device lines; buildDeviceLine does not apply here.
	lines = append(lines,
		"yes - confirm pairing",
		"no  - reject pairing",
	)
	choice, err := m.promptContext(ctx, "Pairing request - "+name, lines)
	if err != nil {
		if ctx.Err() == nil {
			menuStderr("confirm prompt failed: %s", err)
		}
		return false
	}
	return strings.HasPrefix(choice, "yes")
}
