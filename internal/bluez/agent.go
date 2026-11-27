// Package bluez - agent.go implements a BlueZ pairing agent.
//
// BlueZ calls the agent (org.bluez.Agent1) whenever pairing needs a decision.
// Every call runs on its own godbus dispatch goroutine; the agent forwards it
// synchronously to a PairingHandler, which asks the user (menu) or applies a
// policy (CLI), and returns the handler's answer as the D-Bus reply. Each
// request gets its own context, cancelled when BlueZ calls Cancel or Release
// or when agentResponseTimeout expires, so there is no shared state between
// requests that could be lost, mixed up, or left broken by a cancellation.
package bluez

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
	// PairingDisplayPasskey shows Passkey, which the user types on the device
	// (typically a keyboard). BlueZ repeats it as keys are typed (Entered).
	PairingDisplayPasskey
	// PairingDisplayPinCode shows PinCode, which the user types on the device.
	PairingDisplayPinCode
	// PairingEnterPasskey asks for the 6-digit passkey shown on the device.
	PairingEnterPasskey
	// PairingEnterPinCode asks for the device's PIN (legacy devices; usually
	// 0000 or 1234, or a code the user then types on the device).
	PairingEnterPinCode
)

// PairingRequest describes one call BlueZ made into the agent.
type PairingRequest struct {
	Kind    PairingKind
	Device  dbus.ObjectPath
	Address string // MAC address derived from Device
	Passkey uint32 // PairingConfirm and PairingDisplayPasskey
	PinCode string // PairingDisplayPinCode
	Entered uint16 // PairingDisplayPasskey: digits typed on the device so far
}

// Code returns the passkey or PIN to show the user, formatted as BlueZ
// expects it to be typed (passkeys are always six digits).
func (r PairingRequest) Code() string {
	if r.Kind == PairingDisplayPinCode {
		return r.PinCode
	}
	return fmt.Sprintf("%06d", r.Passkey)
}

// PairingHandler decides pairing requests. Its methods are called from D-Bus
// dispatch goroutines and must be safe for concurrent use. ctx is cancelled
// when BlueZ cancels the request or the agent's response timeout expires.
type PairingHandler interface {
	// Confirm returns true to accept a PairingConfirm or PairingAuthorize
	// request.
	Confirm(ctx context.Context, req PairingRequest) bool
	// Display shows the code of a PairingDisplayPasskey or
	// PairingDisplayPinCode request; the user must type it on the device.
	Display(req PairingRequest)
	// Input asks the user for a passkey or PIN (PairingEnterPasskey,
	// PairingEnterPinCode). ok=false rejects the pairing.
	Input(ctx context.Context, req PairingRequest) (value string, ok bool)
}

// Agent implements the org.bluez.Agent1 D-Bus interface on top of a
// PairingHandler. Only methods returning *dbus.Error are exported on the bus.
//
// btctl registers as BlueZ's default agent, so BlueZ routes every pairing
// request to it, including ones started by other nearby devices. The agent
// therefore only consults the handler for devices btctl is currently pairing
// or connecting (see Expect) and rejects everything else.
type Agent struct {
	handler PairingHandler

	mu       sync.Mutex
	client   *Client // set by RegisterAgent; used for logging
	nextID   int
	inflight map[int]context.CancelFunc
	expected map[dbus.ObjectPath]int
}

// NewAgent returns an Agent that consults handler. The caller must invoke
// Client.RegisterAgent to register it with BlueZ.
func NewAgent(handler PairingHandler) *Agent {
	return &Agent{
		handler:  handler,
		inflight: make(map[int]context.CancelFunc),
		expected: make(map[dbus.ObjectPath]int),
	}
}

// Expect allows pairing requests for device until release is called. The
// Client calls it around its own Pair and Connect calls.
func (a *Agent) Expect(device dbus.ObjectPath) (release func()) {
	a.mu.Lock()
	a.expected[device]++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			if a.expected[device]--; a.expected[device] <= 0 {
				delete(a.expected, device)
			}
			a.mu.Unlock()
		})
	}
}

// expects reports whether btctl is currently pairing or connecting device.
func (a *Agent) expects(device dbus.ObjectPath) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expected[device] > 0
}

// rejectUnexpected returns the reply for a request about a device btctl did
// not ask to pair with, or nil when the request may proceed.
func (a *Agent) rejectUnexpected(device dbus.ObjectPath, method string) *dbus.Error {
	if a.expects(device) {
		return nil
	}
	a.debugf("rejecting %s from %s: not a device btctl is pairing", method, device)
	return dbus.NewError(errNameRejected, []any{"Not expecting a pairing request for this device"})
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
func (a *Agent) confirm(req PairingRequest, method string) *dbus.Error {
	if err := a.rejectUnexpected(req.Device, method); err != nil {
		return err
	}
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

// debugf logs agent traffic through the client the agent is registered with
// (only with -v). What the user must act on is shown by the PairingHandler.
func (a *Agent) debugf(format string, args ...any) {
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client != nil {
		client.debugf("agent: "+format, args...)
	}
}

// Release is called by BlueZ when the agent is unregistered.
func (a *Agent) Release() *dbus.Error {
	a.cancelInflight()
	return nil
}

// input asks the handler for a passkey or PIN and validates the answer.
func (a *Agent) input(req PairingRequest, method string, valid func(string) bool) (string, *dbus.Error) {
	if err := a.rejectUnexpected(req.Device, method); err != nil {
		return "", err
	}
	ctx, done := a.begin()
	defer done()
	var value string
	ok := false
	if a.handler != nil {
		value, ok = a.handler.Input(ctx, req)
	}
	value = strings.TrimSpace(value)
	switch {
	case ctx.Err() != nil:
		return "", dbus.NewError(errNameCanceled, []any{"Canceled"})
	case !ok || !valid(value):
		return "", dbus.NewError(errNameRejected, []any{"Rejected"})
	}
	return value, nil
}

// RequestPinCode asks for the PIN of a legacy (pre-2.1) device.
func (a *Agent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	a.debugf("RequestPinCode from %s", device)
	return a.input(PairingRequest{Kind: PairingEnterPinCode, Device: device, Address: deviceLabelFromPath(device)}, "RequestPinCode", validPinCode)
}

// DisplayPinCode shows a PIN the user must type on the device.
func (a *Agent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	a.debugf("DisplayPinCode for %s: %s", device, pincode)
	if err := a.rejectUnexpected(device, "DisplayPinCode"); err != nil {
		return err
	}
	if a.handler != nil {
		a.handler.Display(PairingRequest{Kind: PairingDisplayPinCode, Device: device, Address: deviceLabelFromPath(device), PinCode: pincode})
	}
	return nil
}

// RequestPasskey asks for the 6-digit passkey shown on the device.
func (a *Agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	a.debugf("RequestPasskey from %s", device)
	value, err := a.input(PairingRequest{Kind: PairingEnterPasskey, Device: device, Address: deviceLabelFromPath(device)}, "RequestPasskey", validPasskey)
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseUint(value, 10, 32) // validPasskey guarantees success
	return uint32(n), nil
}

// DisplayPasskey shows a passkey the user must type on the device. BlueZ
// calls it again with a growing entered count as keys are typed.
func (a *Agent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	a.debugf("DisplayPasskey for %s: %06d (entered: %d)", device, passkey, entered)
	if err := a.rejectUnexpected(device, "DisplayPasskey"); err != nil {
		return err
	}
	if a.handler != nil {
		a.handler.Display(PairingRequest{Kind: PairingDisplayPasskey, Device: device, Address: deviceLabelFromPath(device), Passkey: passkey, Entered: entered})
	}
	return nil
}

// validPasskey accepts 1-6 decimal digits (BlueZ passkeys are 0-999999).
func validPasskey(s string) bool {
	if len(s) == 0 || len(s) > 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validPinCode accepts the 1-16 printable ASCII characters BlueZ allows.
func validPinCode(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// RequestConfirmation handles Secure Simple Pairing numeric comparison.
func (a *Agent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.debugf("RequestConfirmation from %s, passkey: %06d", device, passkey)
	return a.confirm(PairingRequest{Kind: PairingConfirm, Device: device, Address: deviceLabelFromPath(device), Passkey: passkey}, "RequestConfirmation")
}

// RequestAuthorization handles Just Works pairing authorization.
func (a *Agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	a.debugf("RequestAuthorization from %s", device)
	return a.confirm(PairingRequest{Kind: PairingAuthorize, Device: device, Address: deviceLabelFromPath(device)}, "RequestAuthorization")
}

// AuthorizeService lets the device btctl is connecting use its profiles.
// BlueZ only asks for untrusted devices; requests from any other device are
// rejected, since accepting them would let any untrusted device in while
// btctl happens to be running.
func (a *Agent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	a.debugf("AuthorizeService from %s, uuid: %s", device, uuid)
	return a.rejectUnexpected(device, "AuthorizeService")
}

// Cancel aborts the requests currently in progress. Later requests are
// handled normally.
func (a *Agent) Cancel() *dbus.Error {
	a.debugf("Cancel")
	a.cancelInflight()
	return nil
}
