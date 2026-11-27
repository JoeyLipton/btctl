// Package bluetooth - properties.go reads and waits on org.bluez.Device1
// properties. watchProperty subscribes to PropertiesChanged AND polls
// concurrently (whichever observes the wanted value first wins) because a
// signal can arrive before the subscription is fully active. The Connect
// completion logic lives here: a new (unbonded) device is only "connected" once
// Connected, Bonded, and Paired are all true, so btctl stays alive through the
// LE bonding handshake instead of exiting at Connected and being dropped.
package bluetooth

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// boolPropertyWant returns a wantFn that matches a bool property value.
func boolPropertyWant(want bool) func(dbus.Variant) bool {
	return func(v dbus.Variant) bool {
		b, ok := v.Value().(bool)
		return ok && b == want
	}
}

// getProperty reads a single property via org.freedesktop.DBus.Properties.Get.
func (c *Client) getProperty(ctx context.Context, path dbus.ObjectPath, iface, property string) (dbus.Variant, error) {
	if c == nil || c.conn == nil {
		return dbus.Variant{}, fmt.Errorf("get property: bluetooth client is not connected")
	}
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	obj := c.conn.Object(bluezService, path)
	call := obj.CallWithContext(callCtx, dbusPropertiesIface+".Get", 0, iface, property)
	if call.Err != nil {
		return dbus.Variant{}, call.Err
	}
	var variant dbus.Variant
	if err := call.Store(&variant); err != nil {
		return dbus.Variant{}, err
	}
	return variant, nil
}

// pollProperty polls a property until wantFn returns true or ctx expires.
func (c *Client) pollProperty(
	ctx context.Context,
	path dbus.ObjectPath,
	iface string,
	property string,
	wantFn func(dbus.Variant) bool,
) error {
	ticker := time.NewTicker(connectedPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll %s on %s: %w", property, path, ctx.Err())
		case <-ticker.C:
			v, err := c.getProperty(ctx, path, iface, property)
			if err != nil {
				if c.verbose {
					fmt.Fprintf(os.Stderr, "%spoll %s on %s: %v (retrying)\n",
						stderrPrefix, property, path, err)
				}
				continue
			}
			if wantFn(v) {
				return nil
			}
		}
	}
}

// propertiesChangedMatchRule returns AddMatchSignal options for PropertiesChanged
// on a single object path.
func propertiesChangedMatchRule(path dbus.ObjectPath) []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(dbusPropertiesIface),
		dbus.WithMatchMember(signalMemberPropertiesChanged),
		dbus.WithMatchSender(bluezService),
	}
}

// parsePropertiesChanged inspects a PropertiesChanged signal for path/iface/
// property. Returns wantMet=true when wantFn matches the new value.
func parsePropertiesChanged(
	sig *dbus.Signal,
	path dbus.ObjectPath,
	iface string,
	property string,
	wantFn func(dbus.Variant) bool,
) (wantMet bool) {
	if sig == nil || sig.Path != path {
		return false
	}
	if len(sig.Body) < 2 {
		return false
	}
	changedIface, ok := sig.Body[0].(string)
	if !ok || changedIface != iface {
		return false
	}
	changed, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}
	if v, ok := changed[property]; ok && wantFn(v) {
		return true
	}
	if propertyWasInvalidated(sig, property) {
		return false
	}
	return false
}

// startPollGoroutine runs pollProperty in a goroutine and sends the result on pollDone.
func startPollGoroutine(
	ctx context.Context,
	c *Client,
	path dbus.ObjectPath,
	iface string,
	property string,
	wantFn func(dbus.Variant) bool,
	pollDone chan<- error,
	wg *sync.WaitGroup,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollDone <- c.pollProperty(ctx, path, iface, property, wantFn)
	}()
}

// watchProperty waits for a property change using concurrent signal delivery
// and active polling. Whichever succeeds first wins.
func (c *Client) watchProperty(
	ctx context.Context,
	path dbus.ObjectPath,
	iface string,
	property string,
	wantFn func(dbus.Variant) bool,
) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("watch %s on %s: bluetooth client is not connected", property, path)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	match := propertiesChangedMatchRule(path)
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return fmt.Errorf("watch %s on %s: subscribe PropertiesChanged: %w", property, path, err)
	}
	defer func() { _ = c.conn.RemoveMatchSignal(match...) }()

	sigCh := make(chan *dbus.Signal, signalChannelBuffer)
	c.conn.Signal(sigCh)
	defer c.conn.RemoveSignal(sigCh)

	pollDone := make(chan error, 1)
	var wg sync.WaitGroup

	startPollGoroutine(watchCtx, c, path, iface, property, wantFn, pollDone, &wg)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-watchCtx.Done():
				return
			case sig := <-sigCh:
				if parsePropertiesChanged(sig, path, iface, property, wantFn) {
					select {
					case pollDone <- nil:
					default:
					}
					return
				}
			}
		}
	}()

	var err error
	select {
	case err = <-pollDone:
	case <-watchCtx.Done():
		err = fmt.Errorf("watch %s on %s: %w", property, path, watchCtx.Err())
	}
	cancel()
	wg.Wait()
	return err
}

// propertyWasInvalidated reports whether property appears in Body[2] without
// a new value, meaning the concurrent poll path should read it.
func propertyWasInvalidated(sig *dbus.Signal, property string) bool {
	if len(sig.Body) < 3 {
		return false
	}
	invalidated, ok := sig.Body[2].([]string)
	if !ok {
		return false
	}
	for _, inv := range invalidated {
		if inv == property {
			return true
		}
	}
	return false
}

// propertyAbsent reports whether err indicates the D-Bus property does not
// exist. BlueZ answers Properties.Get for a missing property with InvalidArgs.
func propertyAbsent(err error) bool {
	return isDBusError(err, errNameInvalidArgs, errNameUnknownProperty)
}

// waitForConnectionComplete waits until the BLE connection is fully established.
// Already-bonded devices need only Connected: true. New devices need Connected,
// then Bonded and Paired within bondingTimeout after Connected is seen.
func (c *Client) waitForConnectionComplete(
	ctx context.Context,
	path dbus.ObjectPath,
	wasAlreadyBonded bool,
) error {
	if wasAlreadyBonded {
		if err := c.watchProperty(ctx, path, bluezDeviceIface, propConnected, boolPropertyWant(true)); err != nil {
			return err
		}
		notify(notifyAppName, "Connected to "+c.getDeviceName(ctx, path))
		return nil
	}

	if err := c.watchProperty(ctx, path, bluezDeviceIface, propConnected, boolPropertyWant(true)); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "%swaiting for bonding to complete...\n", stderrPrefix)

	bondCtx, cancel := context.WithTimeout(ctx, bondingTimeout)
	defer cancel()

	bondedDone := make(chan error, 1)
	pairedDone := make(chan error, 1)

	var bondWg sync.WaitGroup
	bondWg.Add(2)
	go func() {
		defer bondWg.Done()
		bondedDone <- c.watchProperty(bondCtx, path, bluezDeviceIface, propBonded, boolPropertyWant(true))
	}()
	go func() {
		defer bondWg.Done()
		pairedDone <- c.watchProperty(bondCtx, path, bluezDeviceIface, propPaired, boolPropertyWant(true))
	}()

	bondedErr := <-bondedDone
	pairedErr := <-pairedDone

	// Cancel bondCtx so the goroutine that has not yet exited returns
	// from watchProperty promptly, then wait for both to finish cleanup.
	cancel()
	bondWg.Wait()
	name := c.getDeviceName(ctx, path)
	if bondedErr != nil || pairedErr != nil {
		if c.verbose {
			fmt.Fprintf(os.Stderr, "%sbonding timeout for %s, connection may stabilize\n", stderrPrefix, path)
		}
		notify(notifyAppName, "Connected to "+name+" (bonding may still be completing)")
		return nil
	}
	notify(notifyAppName, "Connected to "+name)
	return nil
}

// waitForPaired waits until the Paired property becomes true.
func (c *Client) waitForPaired(ctx context.Context, path dbus.ObjectPath) error {
	return c.watchProperty(ctx, path, bluezDeviceIface, propPaired, boolPropertyWant(true))
}

// waitForServicesResolved waits until ServicesResolved becomes true.
func (c *Client) waitForServicesResolved(ctx context.Context, path dbus.ObjectPath) error {
	return c.watchProperty(ctx, path, bluezDeviceIface, propServicesResolved, boolPropertyWant(true))
}
