// Package bluetooth - device_ops.go implements device mutation: connect,
// disconnect, pair, forget, trust, and agent registration.
package bluetooth

import (
	"context"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// RegisterAgent exports agent under /btctl/agent on the system bus, then
// registers it with BlueZ's AgentManager1 and asks for default-agent priority.
func (c *Client) RegisterAgent(ctx context.Context, agent *Agent) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("register agent: bluetooth client is not connected")
	}
	if agent == nil {
		return fmt.Errorf("register agent: agent is nil")
	}
	if err := c.conn.Export(agent, agentPath, agentInterface); err != nil {
		return fmt.Errorf("register agent: export %s on %s: %w", agentInterface, agentPath, err)
	}
	if err := c.callCtx(ctx, bluezBasePath, agentManagerIface, "RegisterAgent", agentPath, agentCapability); err != nil {
		_ = c.conn.Export(nil, agentPath, agentInterface)
		return fmt.Errorf("register agent: %w", err)
	}
	if err := c.callCtx(ctx, bluezBasePath, agentManagerIface, "RequestDefaultAgent", agentPath); err != nil {
		_ = c.callCtx(ctx, bluezBasePath, agentManagerIface, "UnregisterAgent", agentPath)
		_ = c.conn.Export(nil, agentPath, agentInterface)
		return fmt.Errorf("register agent: request default: %w", err)
	}
	c.mu.Lock()
	c.agent = agent
	c.mu.Unlock()
	return nil
}

// expectPairing tells the registered agent (if any) to accept pairing
// requests for path until the returned function is called.
func (c *Client) expectPairing(path dbus.ObjectPath) (release func()) {
	c.mu.Lock()
	agent := c.agent
	c.mu.Unlock()
	if agent == nil {
		return func() {}
	}
	return agent.Expect(path)
}

// UnregisterAgent removes the agent registration from BlueZ and tears down
// the local D-Bus export.
func (c *Client) UnregisterAgent(ctx context.Context, agent *Agent) error {
	if c == nil || c.conn == nil {
		return nil
	}
	if agent == nil {
		return nil
	}
	c.mu.Lock()
	if c.agent == agent {
		c.agent = nil
	}
	c.mu.Unlock()
	err := c.callCtx(ctx, bluezBasePath, agentManagerIface, "UnregisterAgent", agentPath)
	if err != nil && !isDBusError(err, errNameServiceUnknown) {
		return fmt.Errorf("unregister agent: %w", err)
	}
	if exportErr := c.conn.Export(nil, agentPath, agentInterface); exportErr != nil {
		return fmt.Errorf("unregister agent: remove export: %w", exportErr)
	}
	return nil
}

// ConnectStage identifies the step Connect is about to take.
type ConnectStage int

const (
	// StagePairing: the device is not paired yet and is being paired.
	StagePairing ConnectStage = iota
	// StageConnecting: the device is being connected.
	StageConnecting
)

// ConnectResult describes a successful Connect.
type ConnectResult struct {
	// Paired is true when the device was paired as part of this call.
	Paired bool
	// BondingPending is true when the device connected but BlueZ had not
	// finished bonding when btctl stopped waiting. The connection usually
	// settles on its own.
	BondingPending bool
}

// Connect pairs the device at path if it is not paired yet, marks it trusted
// (so it reconnects on its own later), connects it and waits until the
// connection is usable. progress, when non-nil, is called before each step.
// Only one Connect or Pair per device runs at a time; a concurrent call fails
// with ErrConnectInProgress.
func (c *Client) Connect(ctx context.Context, path dbus.ObjectPath, progress func(ConnectStage)) (ConnectResult, error) {
	var result ConnectResult
	unlock, err := c.lockDevice(path)
	if err != nil {
		return result, err
	}
	defer unlock()
	if progress == nil {
		progress = func(ConnectStage) {}
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeouts.connection)
	defer cancel()

	paired, err := c.boolProperty(ctx, path, propPaired)
	if err != nil {
		return result, fmt.Errorf("connect device %s: %w", path, err)
	}
	if !paired {
		progress(StagePairing)
		if err := c.pairDevice(ctx, path, true); err != nil {
			return result, err
		}
		result.Paired = true
	}
	progress(StageConnecting)
	result.BondingPending, err = c.connectDevice(ctx, path)
	return result, err
}

// Pair pairs and trusts the device at path without connecting it.
func (c *Client) Pair(ctx context.Context, path dbus.ObjectPath) error {
	unlock, err := c.lockDevice(path)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, c.timeouts.connection)
	defer cancel()
	return c.pairDevice(ctx, path, false)
}

// lockDevice claims the per-device lock without waiting.
func (c *Client) lockDevice(path dbus.ObjectPath) (unlock func(), err error) {
	mu := c.connLock(path)
	if !mu.TryLock() {
		return nil, fmt.Errorf("%w: %s", ErrConnectInProgress, path)
	}
	return mu.Unlock, nil
}

// pairDevice runs the BlueZ pairing handshake and waits for Paired: true.
// Trusted is set first. While the Pair() call is in flight BlueZ asks the
// registered agent, whose PairingHandler answers. With waitServices, it then
// waits (bounded) for service discovery so an immediate Connect finds the
// device's profiles.
func (c *Client) pairDevice(ctx context.Context, path dbus.ObjectPath, waitServices bool) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("pair device: bluetooth client is not connected")
	}
	defer c.expectPairing(path)()

	c.trustWithWarning(ctx, path)

	// The watcher is cancelled and joined on every path so it never keeps
	// polling after pairDevice returns.
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pairedDone := make(chan error, 1)
	go func() {
		pairedDone <- c.waitForPaired(watchCtx, path)
	}()

	if err := c.pairDBusCall(ctx, path); err != nil {
		cancel()
		<-pairedDone
		return err
	}

	if err := <-pairedDone; err != nil {
		return fmt.Errorf("pair device %s: %w", path, err)
	}

	if !waitServices {
		return nil
	}
	svcCtx, svcCancel := context.WithTimeout(ctx, c.timeouts.servicesResolved)
	defer svcCancel()
	if err := c.waitForServicesResolved(svcCtx, path); err != nil && c.verbose {
		fmt.Fprintf(os.Stderr, "%sServicesResolved did not become true: %v\n", stderrPrefix, err)
	}
	return nil
}

// pairDBusCall invokes Device1.Pair with the pair timeout.
func (c *Client) pairDBusCall(ctx context.Context, path dbus.ObjectPath) error {
	err := c.callTimeout(ctx, c.timeouts.pair, path, bluezDeviceIface, "Pair", nil)
	if err != nil && !isDBusError(err, errNameAlreadyExists) {
		return fmt.Errorf("pair device %s: %w", path, err)
	}
	return nil
}

// connectDevice sets Trusted, invokes Device1.Connect and waits until the
// connection is complete (Connected for a bonded device; Connected plus
// Bonded and Paired otherwise). It reports whether bonding was still pending.
func (c *Client) connectDevice(ctx context.Context, path dbus.ObjectPath) (bondingPending bool, err error) {
	// Connecting a device whose bond was lost makes BlueZ pair again.
	defer c.expectPairing(path)()

	wasAlreadyBonded, err := c.boolProperty(ctx, path, propBonded)
	if err != nil && c.verbose {
		fmt.Fprintf(os.Stderr, "%sread Bonded on %s: %v\n", stderrPrefix, path, err)
	}
	c.trustWithWarning(ctx, path)

	waitCtx, cancel := context.WithTimeout(ctx, c.timeouts.connection)
	defer cancel()
	type outcome struct {
		pending bool
		err     error
	}
	watchDone := make(chan outcome, 1)
	go func() {
		pending, err := c.waitForConnectionComplete(waitCtx, path, wasAlreadyBonded)
		watchDone <- outcome{pending, err}
	}()
	// Device1.Connect blocks until the link is up, so it gets the connect
	// timeout rather than the short per-call one.
	err = c.callTimeout(ctx, c.timeouts.connect, path, bluezDeviceIface, "Connect", nil)
	if err != nil && !isDBusError(err, errNameAlreadyConnected) {
		cancel()
		<-watchDone
		return false, fmt.Errorf("connect device %s: %w", path, err)
	}
	done := <-watchDone
	if done.err != nil {
		return false, fmt.Errorf("connect device %s: %w", path, done.err)
	}
	return done.pending, nil
}

// Disconnect asks BlueZ to disconnect from the device at path and waits for
// Connected: false. A device that is not connected counts as success.
func (c *Client) Disconnect(ctx context.Context, path dbus.ObjectPath) error {
	// The watch is bounded by the caller's context too, and is always joined
	// before returning so no goroutine outlives the call.
	disconnectCtx, cancel := context.WithTimeout(ctx, c.timeouts.disconnect)
	defer cancel()
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- c.watchProperty(disconnectCtx, path, bluezDeviceIface, propConnected, boolPropertyWant(false))
	}()
	if err := c.callCtx(ctx, path, bluezDeviceIface, "Disconnect"); err != nil {
		cancel()
		<-watchDone
		if isDBusError(err, errNameNotConnected) {
			return nil
		}
		return fmt.Errorf("disconnect device %s: %w", path, err)
	}
	if err := <-watchDone; err != nil {
		return fmt.Errorf("disconnect device %s: %w", path, err)
	}
	return nil
}

// ForgetDevice removes a device from BlueZ (unpairing it) via RemoveDevice
// on the active adapter.
func (c *Client) ForgetDevice(ctx context.Context, path dbus.ObjectPath) error {
	adapterPath, _, err := c.findAdapterPath(ctx)
	if err != nil {
		return fmt.Errorf("forget device %s: %w", path, err)
	}
	if err := c.callCtx(ctx, adapterPath, bluezAdapterIface, "RemoveDevice", path); err != nil {
		return fmt.Errorf("forget device %s: %w", path, err)
	}
	return nil
}

// SetTrusted writes the Trusted property on org.bluez.Device1 at path.
func (c *Client) SetTrusted(ctx context.Context, path dbus.ObjectPath, trusted bool) error {
	if err := c.callCtx(ctx, path, dbusPropertiesIface, "Set", bluezDeviceIface, propTrusted, dbus.MakeVariant(trusted)); err != nil {
		return fmt.Errorf("set trusted %s: %w", path, err)
	}
	return nil
}

// boolProperty reads a boolean org.bluez.Device1 property. A missing
// property reads as false.
func (c *Client) boolProperty(ctx context.Context, path dbus.ObjectPath, property string) (bool, error) {
	v, err := c.getProperty(ctx, path, bluezDeviceIface, property)
	if err != nil {
		if propertyAbsent(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s on %s: %w", property, path, err)
	}
	b, _ := v.Value().(bool)
	return b, nil
}

// trustWithWarning sets Trusted=true; failures are logged, not returned,
// because a connection still works without it.
func (c *Client) trustWithWarning(ctx context.Context, path dbus.ObjectPath) {
	if err := c.SetTrusted(ctx, path, true); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: could not mark %s trusted: %s\n", stderrPrefix, deviceLabelFromPath(path), FormatUserError(err))
	}
}
