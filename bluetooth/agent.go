// Package bluetooth - agent.go implements a BlueZ pairing agent.
//
// BlueZ calls the agent (org.bluez.Agent1) whenever pairing needs a decision.
// Every call runs on its own godbus dispatch goroutine; the agent forwards it
// synchronously to a PairingHandler, which asks the user (menu) or applies a
// policy (CLI), and returns the handler's answer as the D-Bus reply. Each
// request gets its own context, cancelled when BlueZ calls Cancel or Release
// or when agentResponseTimeout expires, so there is no shared state between
// requests that could be lost, mixed up, or left broken by a cancellation.
package bluetooth

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// PairingKind identifies what BlueZ is asking the agent for.
type PairingKind int

const (
	// PairingConfirm asks whether Passkey matches the one shown on the device
	// (Secure Simple Pairing numeric comparison).
	PairingConfirm PairingKind = iota
	// PairingAuthorize asks whether to allow a pairing that has no passkey
	// (Just Works).
	PairingAuthorize
)

// PairingRequest describes one call BlueZ made into the agent.
type PairingRequest struct {
	Kind    PairingKind
	Device  dbus.ObjectPath
	Address string // MAC address derived from Device
	Passkey uint32 // for PairingConfirm
}

// PairingHandler decides pairing requests. Its methods are called from D-Bus
// dispatch goroutines and must be safe for concurrent use. ctx is cancelled
// when BlueZ cancels the request or the agent's response timeout expires.
type PairingHandler interface {
	// Confirm returns true to accept a PairingConfirm or PairingAuthorize
	// request.
	Confirm(ctx context.Context, req PairingRequest) bool
}

// Agent implements the org.bluez.Agent1 D-Bus interface on top of a
// PairingHandler. Only methods returning *dbus.Error are exported on the bus.
type Agent struct {
	handler PairingHandler

	mu       sync.Mutex
	nextID   int
	inflight map[int]context.CancelFunc
}

// NewAgent returns an Agent that consults handler. The caller must invoke
// Client.RegisterAgent to register it with BlueZ.
func NewAgent(handler PairingHandler) *Agent {
	return &Agent{handler: handler, inflight: make(map[int]context.CancelFunc)}
}

// begin starts tracking a request and returns its context and a release func.
func (a *Agent) begin() (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), agentResponseTimeout)
	a.mu.Lock()
	id := a.nextID
	a.nextID++
	a.inflight[id] = cancel
	a.mu.Unlock()
	return ctx, func() {
		a.mu.Lock()
		delete(a.inflight, id)
		a.mu.Unlock()
		cancel()
	}
}

// cancelInflight cancels every request currently being handled.
func (a *Agent) cancelInflight() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.inflight {
		cancel()
	}
}

// confirm runs a yes/no request through the handler and maps the answer to
// the D-Bus reply BlueZ expects.
func (a *Agent) confirm(req PairingRequest) *dbus.Error {
	ctx, done := a.begin()
	defer done()
	accepted := a.handler != nil && a.handler.Confirm(ctx, req)
	switch {
	case ctx.Err() != nil:
		return dbus.NewError(errNameCanceled, []any{"Canceled"})
	case accepted:
		return nil
	default:
		return dbus.NewError(errNameRejected, []any{"Rejected"})
	}
}

// agentLogAlways writes unconditionally. Use for rare but significant
// events: PIN entry, passkey confirmation, authorization requests.
func agentLogAlways(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[agent] "+format+"\n", args...)
}

// agentLogVerbose writes only when the package verbose flag is set.
// Use for high-frequency events: AuthorizeService, DisplayPasskey.
func agentLogVerbose(format string, args ...interface{}) {
	if notifyVerbose {
		fmt.Fprintf(os.Stderr, "[agent] "+format+"\n", args...)
	}
}

// Release is called by BlueZ when the agent is unregistered.
func (a *Agent) Release() *dbus.Error {
	a.cancelInflight()
	return nil
}

// RequestPinCode handles legacy PIN-based pairing.
func (a *Agent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	agentLogAlways("RequestPinCode from %s", device)
	return defaultPinCode, nil
}

// DisplayPinCode logs a PIN chosen by BlueZ.
func (a *Agent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	agentLogAlways("PIN for %s: %s", device, pincode)
	return nil
}

// RequestPasskey handles legacy passkey entry flows. Passkey entry is not
// supported yet, so the request is rejected immediately rather than left to
// time out.
func (a *Agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	agentLogAlways("RequestPasskey from %s", device)
	return 0, dbus.NewError(errNameRejected, []any{"Passkey entry not supported"})
}

// DisplayPasskey logs a passkey for the user to verify.
func (a *Agent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	agentLogVerbose("Passkey for %s: %d (entered: %d)", device, passkey, entered)
	return nil
}

// RequestConfirmation handles Secure Simple Pairing numeric comparison.
func (a *Agent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	agentLogAlways("RequestConfirmation from %s, passkey: %06d", device, passkey)
	return a.confirm(PairingRequest{Kind: PairingConfirm, Device: device, Address: deviceLabelFromPath(device), Passkey: passkey})
}

// RequestAuthorization handles Just Works pairing authorization.
func (a *Agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	agentLogAlways("RequestAuthorization from %s", device)
	return a.confirm(PairingRequest{Kind: PairingAuthorize, Device: device, Address: deviceLabelFromPath(device)})
}

// AuthorizeService accepts per-profile access for paired devices.
func (a *Agent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	agentLogVerbose("AuthorizeService from %s, uuid: %s", device, uuid)
	return nil
}

// Cancel aborts the requests currently in progress. Later requests are
// handled normally.
func (a *Agent) Cancel() *dbus.Error {
	agentLogVerbose("Cancel")
	a.cancelInflight()
	return nil
}

// AutoConfirmHandler accepts every request. Non-interactive commands use it
// because there is no picker to ask the user.
type AutoConfirmHandler struct{}

// Confirm accepts the request and tells the user it did so.
func (AutoConfirmHandler) Confirm(_ context.Context, req PairingRequest) bool {
	agentLogVerbose("auto-confirming pairing request from %s", req.Address)
	notify(notifyAppName, "Auto-authorized connection from "+req.Address)
	return true
}
