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
	return nil
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
	err := c.callCtx(ctx, bluezBasePath, agentManagerIface, "UnregisterAgent", agentPath)
	if err != nil && !isDBusError(err, errNameServiceUnknown) {
		return fmt.Errorf("unregister agent: %w", err)
	}
	if exportErr := c.conn.Export(nil, agentPath, agentInterface); exportErr != nil {
		return fmt.Errorf("unregister agent: remove export: %w", exportErr)
	}
	return nil
}

// PairDevice runs the BlueZ pairing handshake and waits for Paired: true.
// Connect() must be called separately after PairDevice returns. Trusted is set
// before Pair(). While the Pair() call is in flight BlueZ asks the registered
// agent, whose PairingHandler answers.
func (c *Client) PairDevice(ctx context.Context, path dbus.ObjectPath) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("pair device: bluetooth client is not connected")
	}

	c.trustWithWarning(ctx, path, string(path))

	pairedDone := make(chan error, 1)
	go func() {
		pairedDone <- c.waitForPaired(ctx, path)
	}()

	if err := c.pairDBusCall(ctx, path); err != nil {
		return err
	}

	if err := <-pairedDone; err != nil {
		return fmt.Errorf("pair device %s: %w", path, err)
	}

	svcCtx, svcCancel := context.WithTimeout(ctx, c.timeouts.servicesResolved)
	defer svcCancel()
	if err := c.waitForServicesResolved(svcCtx, path); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: ServicesResolved did not resolve: %v\n", stderrPrefix, err)
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

// Connect sets Trusted, invokes Device1.Connect, then waits until the
// connection is complete (Connected only if already bonded; otherwise
// Connected plus Bonded and Paired).
func (c *Client) Connect(ctx context.Context, path dbus.ObjectPath) error {
	mu := c.connLock(path)
	if !mu.TryLock() {
		return fmt.Errorf("%w: %s", ErrConnectInProgress, path)
	}
	defer mu.Unlock()
	return c.connectDirect(ctx, path)
}

// connectDirect is identical to Connect but without connLock. Menu callers
// that hold connLock externally use this to avoid double-locking.
func (c *Client) connectDirect(ctx context.Context, path dbus.ObjectPath) error {
	wasAlreadyBonded, err := c.isDeviceBonded(ctx, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: read bonded %s: %v\n", stderrPrefix, path, err)
		wasAlreadyBonded = false
	}

	if err := c.SetTrusted(ctx, path, true); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: set trusted %s: %v\n", stderrPrefix, path, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, c.timeouts.connection)
	defer cancel()
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- c.waitForConnectionComplete(waitCtx, path, wasAlreadyBonded)
	}()
	// Device1.Connect blocks until the link is up, so it gets the connect
	// timeout rather than the short per-call one.
	err = c.callTimeout(ctx, c.timeouts.connect, path, bluezDeviceIface, "Connect", nil)
	if err != nil && !isDBusError(err, errNameAlreadyConnected) {
		cancel()
		<-watchDone
		notify(notifyAppName, "Could not connect to "+c.getDeviceName(ctx, path)+": "+FormatUserError(err))
		return fmt.Errorf("connect device %s: %w", path, err)
	}
	if err := <-watchDone; err != nil {
		notify(notifyAppName, "Could not connect to "+c.getDeviceName(ctx, path)+": "+FormatUserError(err))
		return fmt.Errorf("connect device %s: %w", path, err)
	}
	return nil
}

// connectInteractive fires Device1.Connect without Connected polling, for
// interactive menu pairing flows.
func (c *Client) connectInteractive(ctx context.Context, path dbus.ObjectPath) error {
	if err := c.callTimeout(ctx, c.timeouts.connect, path, bluezDeviceIface, "Connect", nil); err != nil {
		return fmt.Errorf("connect device %s: %w", path, err)
	}
	return nil
}

// Disconnect asks BlueZ to disconnect from the device at path. Property watch
// for Connected: false is subscribed before the Disconnect() D-Bus call.
func (c *Client) Disconnect(ctx context.Context, path dbus.ObjectPath) error {
	name := c.getDeviceName(ctx, path)

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
	notify(notifyAppName, "Disconnected from "+name)
	return nil
}

// ForgetDevice removes a paired device from BlueZ via RemoveDevice on the
// active adapter.
func (c *Client) ForgetDevice(ctx context.Context, path dbus.ObjectPath) error {
	name := c.getDeviceName(ctx, path)

	adapterPath, _, err := c.findAdapterPath(ctx)
	if err != nil {
		return fmt.Errorf("forget device %s: %w", path, err)
	}
	if err := c.callCtx(ctx, adapterPath, bluezAdapterIface, "RemoveDevice", path); err != nil {
		return fmt.Errorf("forget device %s: %w", path, err)
	}
	notify(notifyAppName, "Removed "+name)
	return nil
}

// SetTrusted writes the Trusted property on org.bluez.Device1 at path.
func (c *Client) SetTrusted(ctx context.Context, path dbus.ObjectPath, trusted bool) error {
	if err := c.callCtx(ctx, path, dbusPropertiesIface, "Set", bluezDeviceIface, "Trusted", dbus.MakeVariant(trusted)); err != nil {
		return fmt.Errorf("set trusted %s: %w", path, err)
	}
	return nil
}

// isDeviceBonded reads the Bonded property on org.bluez.Device1. A missing
// property is treated as not bonded.
func (c *Client) isDeviceBonded(ctx context.Context, path dbus.ObjectPath) (bool, error) {
	v, err := c.getProperty(ctx, path, bluezDeviceIface, propBonded)
	if err != nil {
		if propertyAbsent(err) {
			return false, nil
		}
		return false, fmt.Errorf("read bonded %s: %w", path, err)
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, nil
	}
	return b, nil
}

// trustWithWarning sets Trusted=true before pair; failures are logged and not returned.
func (c *Client) trustWithWarning(ctx context.Context, path dbus.ObjectPath, label string) {
	if c == nil {
		return
	}
	if err := c.SetTrusted(ctx, path, true); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: set trusted %s: %v\n", stderrPrefix, label, err)
	}
}
