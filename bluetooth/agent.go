// Package bluetooth - agent.go implements a BlueZ pairing agent.
// The agent handles confirmation requests from BlueZ during device pairing. It
// does not implement real PIN entry or passkey display beyond logging; modern
// BLE devices use Just Works or Numeric Comparison pairing, which only requires
// a yes/no confirmation. The interactive menu drives that confirmation through
// WaitForConfirmation/Confirm/Reject; non-interactive commands use AutoConfirm.
package bluetooth

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// pairingRequest is the payload delivered to the menu layer when BlueZ
// asks the agent to confirm pairing for a device.
type pairingRequest struct {
	device  dbus.ObjectPath
	passkey uint32
}

// Agent implements the org.bluez.Agent1 D-Bus interface. BlueZ calls into
// this object whenever it needs a pairing decision.
type Agent struct {
	conn *dbus.Conn

	mu         sync.Mutex
	pairingDev dbus.ObjectPath
	requestCh  chan pairingRequest
	confirmCh  chan bool
	passkeyCh  chan uint32
	cancelCh   chan struct{}
}

// NewAgent constructs an Agent bound to conn. The caller must invoke
// Client.RegisterAgent to register it with BlueZ.
func NewAgent(conn *dbus.Conn) *Agent {
	return &Agent{
		conn:      conn,
		requestCh: make(chan pairingRequest),
		confirmCh: make(chan bool),
		passkeyCh: make(chan uint32),
		cancelCh:  make(chan struct{}),
	}
}

// snapshotChans returns the agent's current channel set under mu.
func (a *Agent) snapshotChans() (chan pairingRequest, chan bool, chan uint32, chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requestCh, a.confirmCh, a.passkeyCh, a.cancelCh
}

func (a *Agent) setPairingDevice(device dbus.ObjectPath) {
	a.mu.Lock()
	a.pairingDev = device
	a.mu.Unlock()
}

func (a *Agent) clearPairingDevice() {
	a.mu.Lock()
	a.pairingDev = ""
	a.mu.Unlock()
}

// PairingDevice reports the object path of the device currently being
// paired, or "" if no request is in progress.
func (a *Agent) PairingDevice() dbus.ObjectPath {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pairingDev
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
	a.reopenCancelCh()
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

// RequestPasskey handles legacy passkey entry flows.
func (a *Agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	agentLogAlways("RequestPasskey from %s", device)
	a.setPairingDevice(device)
	defer a.clearPairingDevice()

	_, _, passkeyCh, cancelCh := a.snapshotChans()
	select {
	case p := <-passkeyCh:
		return p, nil
	case <-cancelCh:
		return 0, &dbus.Error{Name: errNameCanceled}
	case <-time.After(agentResponseTimeout):
		return 0, &dbus.Error{Name: errNameCanceled}
	}
}

// DisplayPasskey logs a passkey for the user to verify.
func (a *Agent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	agentLogVerbose("Passkey for %s: %d (entered: %d)", device, passkey, entered)
	return nil
}

// RequestConfirmation forwards a Secure Simple Pairing confirmation request
// to the menu or AutoConfirm loop.
func (a *Agent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	agentLogAlways("RequestConfirmation from %s, passkey: %d", device, passkey)
	notifyAlert("btctl - pairing request", "Authorize connection from "+deviceLabelFromPath(device)+"?")
	a.setPairingDevice(device)
	defer a.clearPairingDevice()

	requestCh, confirmCh, _, cancelCh := a.snapshotChans()
	select {
	case requestCh <- pairingRequest{device: device, passkey: passkey}:
	case <-cancelCh:
		return &dbus.Error{Name: errNameCanceled}
	case <-time.After(agentResponseTimeout):
		return &dbus.Error{Name: errNameCanceled}
	}
	select {
	case decision := <-confirmCh:
		if decision {
			return nil
		}
		return &dbus.Error{Name: errNameRejected}
	case <-cancelCh:
		return &dbus.Error{Name: errNameCanceled}
	case <-time.After(agentResponseTimeout):
		return &dbus.Error{Name: errNameCanceled}
	}
}

// RequestAuthorization handles Just Works pairing authorization.
func (a *Agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	agentLogAlways("RequestAuthorization from %s", device)
	notifyAlert("btctl - pairing request", "Authorize connection from "+deviceLabelFromPath(device)+"?")
	a.setPairingDevice(device)
	defer a.clearPairingDevice()

	requestCh, confirmCh, _, cancelCh := a.snapshotChans()
	select {
	case requestCh <- pairingRequest{device: device, passkey: 0}:
	case <-cancelCh:
		return &dbus.Error{Name: errNameCanceled}
	case <-time.After(agentResponseTimeout):
		return &dbus.Error{Name: errNameCanceled}
	}
	select {
	case decision := <-confirmCh:
		if decision {
			return nil
		}
		return &dbus.Error{Name: errNameRejected}
	case <-cancelCh:
		return &dbus.Error{Name: errNameCanceled}
	case <-time.After(agentResponseTimeout):
		return &dbus.Error{Name: errNameCanceled}
	}
}

// AuthorizeService accepts per-profile access for paired devices.
func (a *Agent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	agentLogVerbose("AuthorizeService from %s, uuid: %s", device, uuid)
	return nil
}

// Cancel aborts a pending agent request.
func (a *Agent) Cancel() *dbus.Error {
	agentLogVerbose("Cancel")
	a.reopenCancelCh()
	return nil
}

// reopenCancelCh replaces cancelCh with a fresh unbuffered channel under a.mu.
func (a *Agent) reopenCancelCh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	closeIfOpen(a.cancelCh)
	a.cancelCh = make(chan struct{})
}

// WaitForConfirmation blocks until BlueZ issues a confirmation request,
// ctx is done, or the agent is canceled.
func (a *Agent) WaitForConfirmation(ctx context.Context) (bool, uint32, error) {
	requestCh, _, _, cancelCh := a.snapshotChans()
	select {
	case req := <-requestCh:
		return true, req.passkey, nil
	case <-cancelCh:
		return false, 0, ErrAgentCanceled
	case <-ctx.Done():
		return false, 0, ctx.Err()
	}
}

// Confirm signals accept pairing for the current request.
func (a *Agent) Confirm() {
	_, confirmCh, _, _ := a.snapshotChans()
	safeSendBool(confirmCh, true)
}

// Reject signals reject pairing for the current request.
func (a *Agent) Reject() {
	_, confirmCh, _, _ := a.snapshotChans()
	safeSendBool(confirmCh, false)
}

// AutoConfirm auto-accepts every confirmation request until ctx is done.
//
// It is used by the non-interactive CLI commands (connect, the scan
// connect prompt) where there is no picker to ask the user. The interactive
// `btctl pick` menu does the opposite: it relays each request to a yes/no
// picker via WaitForConfirmation, so the two paths are mutually exclusive and
// only one consumes the agent's request channel at a time.
func (a *Agent) AutoConfirm(ctx context.Context) {
	for {
		needsConfirm, _, err := a.WaitForConfirmation(ctx)
		if err != nil {
			return
		}
		if needsConfirm {
			agentLogVerbose("auto-confirming pairing request")
			notify(notifyAppName, "Auto-authorized connection from "+deviceLabelFromPath(a.PairingDevice()))
			a.Confirm()
		}
	}
}

// safeSendBool sends v on ch without blocking if the receiver has exited.
func safeSendBool(ch chan<- bool, v bool) {
	select {
	case ch <- v:
	default:
	}
}

// closeIfOpen closes ch if it has not already been closed.
func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
