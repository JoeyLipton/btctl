// Package bluetooth - connect_flow.go drives menu connect paths that may
// need interactive pairing confirmation via the BlueZ agent.
package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// confirmInfo is delivered when the agent receives a pairing request or
// terminates with an error.
type confirmInfo struct {
	passkey uint32
	err     error
}

type connectEventKind int

const (
	connectEventDBusDone connectEventKind = iota
	connectEventConnected
	connectEventConfirm
	connectEventCtxDone
)

type connectEvent struct {
	kind    connectEventKind
	err     error
	connErr error
	confirm confirmInfo
}

// connectWithConfirmation is the shared connect path for menu flows that
// may need interactive pairing confirmation.
func (m *Menu) connectWithConfirmation(parentCtx context.Context, device Device) error {
	mu := m.client.connLock(device.Path)
	if !mu.TryLock() {
		fmt.Fprintf(os.Stderr, "%salready connecting to %s\n", stderrPrefix, device.Name)
		return nil
	}
	defer mu.Unlock()

	wasAlreadyBonded, err := m.client.isDeviceBonded(parentCtx, device.Path)
	if err != nil {
		wasAlreadyBonded = false
	}
	if err := m.client.SetTrusted(parentCtx, device.Path, true); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: set trusted %s: %v\n", stderrPrefix, device.Address, err)
	}

	if m.agent == nil {
		return m.connectWithoutAgent(parentCtx, device)
	}
	return m.connectWithAgent(parentCtx, device, wasAlreadyBonded)
}

// connectWithoutAgent connects when no pairing agent is registered.
func (m *Menu) connectWithoutAgent(parentCtx context.Context, device Device) error {
	name := devicePresentName(device)
	addr := device.Address
	if !device.Paired {
		menuStderr("pairing with %s (%s)...", name, addr)
		if err := m.client.PairDevice(parentCtx, device.Path); err != nil {
			menuStderr("error: %s", err)
			notify(notifyAppName, "Could not connect to "+name+": "+FormatUserError(err))
			return nil
		}
	}
	menuStderr("connecting to %s (%s)...", name, addr)
	if err := m.client.connectDirect(parentCtx, device.Path); err != nil {
		menuStderr("error: %s", err)
		return nil
	}
	menuStderr("connected to %s (%s)", name, addr)
	return nil
}

// connectWithAgent drives pairing then connection with agent confirmation
// during both Pair() and Connect().
func (m *Menu) connectWithAgent(parentCtx context.Context, device Device, wasAlreadyBonded bool) error {
	name := devicePresentName(device)
	addr := device.Address

	ctx, cancel := context.WithTimeout(parentCtx, pairingTimeout)
	var wg sync.WaitGroup
	// cancel must run before wg.Wait to unblock goroutines waiting on
	// ctx.Done. Defers run LIFO so cancel() is written after wg.Wait().
	defer wg.Wait()
	defer cancel()

	confirmCh := make(chan confirmInfo, 4)
	m.startAgentConfirmRelay(ctx, &wg, confirmCh)

	if !device.Paired {
		if err := m.runPairingPhase(ctx, &wg, device, confirmCh, name, addr); err != nil {
			return nil
		}
	}

	return m.runConnectPhase(ctx, &wg, device, confirmCh, name, addr, wasAlreadyBonded)
}

// startAgentConfirmRelay forwards agent pairing requests to confirmCh.
func (m *Menu) startAgentConfirmRelay(ctx context.Context, wg *sync.WaitGroup, confirmCh chan<- confirmInfo) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			needs, passkey, err := m.agent.WaitForConfirmation(ctx)
			if err != nil {
				select {
				case confirmCh <- confirmInfo{err: err}:
				case <-ctx.Done():
				}
				return
			}
			if !needs {
				continue
			}
			select {
			case confirmCh <- confirmInfo{passkey: passkey}:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// runPairingPhase calls PairDevice while handling agent confirmation prompts.
func (m *Menu) runPairingPhase(
	ctx context.Context,
	wg *sync.WaitGroup,
	device Device,
	confirmCh <-chan confirmInfo,
	name, addr string,
) error {
	menuStderr("pairing with %s (%s)...", name, addr)

	pairDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		pairDone <- m.client.PairDevice(ctx, device.Path)
	}()

	for {
		select {
		case err := <-pairDone:
			if err != nil {
				menuStderr("error: %s", err)
				return err
			}
			return nil
		case cr := <-confirmCh:
			if cr.err != nil {
				if errors.Is(cr.err, ErrAgentCanceled) {
					menuStderr("pairing canceled")
				} else {
					menuStderr("pairing timed out")
				}
				return cr.err
			}
			if !m.confirmPairingPrompt(device, cr.passkey) {
				m.agent.Reject()
				menuStderr("pairing rejected")
				return fmt.Errorf("pairing rejected")
			}
			m.agent.Confirm()
		case <-ctx.Done():
			menuStderr("pairing timed out")
			return ctx.Err()
		}
	}
}

// runConnectPhase calls Connect after the connection-complete watch is subscribed.
func (m *Menu) runConnectPhase(
	ctx context.Context,
	wg *sync.WaitGroup,
	device Device,
	confirmCh <-chan confirmInfo,
	name, addr string,
	wasAlreadyBonded bool,
) error {
	menuStderr("connecting to %s (%s)...", name, addr)

	connectedCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		connectedCh <- m.client.waitForConnectionComplete(ctx, device.Path, wasAlreadyBonded)
	}()

	connectDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		connectDone <- m.client.connectInteractive(ctx, device.Path)
	}()

	for {
		select {
		case err := <-connectDone:
			connectDone = nil
			ev := connectEvent{kind: connectEventDBusDone, err: err}
			if done, _ := m.handleConnectEvent(ctx, device, ev, name, addr); done {
				return nil
			}
		case connErr := <-connectedCh:
			ev := connectEvent{kind: connectEventConnected, connErr: connErr}
			if done, _ := m.handleConnectEvent(ctx, device, ev, name, addr); done {
				return nil
			}
		case cr := <-confirmCh:
			ev := connectEvent{kind: connectEventConfirm, confirm: cr}
			if done, _ := m.handleConnectEvent(ctx, device, ev, name, addr); done {
				return nil
			}
		case <-ctx.Done():
			ev := connectEvent{kind: connectEventCtxDone}
			if done, _ := m.handleConnectEvent(ctx, device, ev, name, addr); done {
				return nil
			}
		}
	}
}

// handleConnectEvent processes one event from the connect select loop.
func (m *Menu) handleConnectEvent(
	ctx context.Context,
	device Device,
	ev connectEvent,
	name, addr string,
) (done bool, err error) {
	_ = ctx
	switch ev.kind {
	case connectEventDBusDone:
		if ev.err != nil && !isDBusError(ev.err, errNameAlreadyConnected) {
			menuStderr("error: %s", ev.err)
			notify(notifyAppName, "Could not connect to "+name+": "+FormatUserError(ev.err))
			return true, nil
		}
		return false, nil
	case connectEventConnected:
		if ev.connErr == nil {
			menuStderr("connected to %s (%s)", name, addr)
			return true, nil
		}
		menuStderr("error: connect timed out")
		notify(notifyAppName, "Could not connect to "+name+": connect timed out")
		return true, nil
	case connectEventConfirm:
		cr := ev.confirm
		if cr.err != nil {
			if errors.Is(cr.err, ErrAgentCanceled) {
				menuStderr("pairing canceled")
			} else {
				menuStderr("connection timed out")
			}
			return true, nil
		}
		if !m.confirmPairingPrompt(device, cr.passkey) {
			m.agent.Reject()
			menuStderr("pairing rejected")
			return true, nil
		}
		m.agent.Confirm()
		menuStderr("pairing confirmed, waiting for connection...")
		return false, nil
	case connectEventCtxDone:
		menuStderr("connection timed out")
		notify(notifyAppName, "Could not connect to "+name+": connection timed out")
		return true, nil
	default:
		return false, nil
	}
}

// confirmPairingPrompt opens the yes/no confirmation picker for an
// in-flight pairing request and returns true iff the user accepted.
func (m *Menu) confirmPairingPrompt(device Device, passkey uint32) bool {
	var lines []string
	if passkey != 0 {
		lines = append(lines, fmt.Sprintf("Passkey: %d", passkey))
	}
	// Static prompt line, not a device line; buildDeviceLine does not apply here.
	lines = append(lines,
		"yes - confirm pairing",
		"no  - reject pairing",
	)
	title := fmt.Sprintf("Pairing request - %s", devicePresentName(device))
	choice, err := m.prompt(title, lines)
	if err != nil {
		menuStderr("confirm prompt failed: %s", err)
		return false
	}
	return strings.HasPrefix(choice, "yes")
}
