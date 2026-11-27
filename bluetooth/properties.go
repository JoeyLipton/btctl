// Package bluetooth - properties.go reads and waits on org.bluez.Device1
// properties. watchProperty is driven by PropertiesChanged signals (see its
// comment for why no change can be missed). The Connect completion logic lives
// here: a new (unbonded) device is only "connected" once Connected, Bonded,
// and Paired are all true, so btctl stays alive through the LE bonding
// handshake instead of exiting at Connected and being dropped.
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
	callCtx, cancel := context.WithTimeout(ctx, c.timeouts.call)
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

// propertyChange classifies a signal for one property of one object.
type propertyChange int

const (
	propertyUnrelated   propertyChange = iota // another object, interface or property
	propertyWanted                            // the new value satisfies wantFn
	propertyOther                             // a new value that does not satisfy wantFn
	propertyInvalidated                       // changed without a value; re-read it
)

// classifyPropertyChange inspects a PropertiesChanged signal for
// path/iface/property.
func classifyPropertyChange(
	sig *dbus.Signal,
	path dbus.ObjectPath,
	iface string,
	property string,
	wantFn func(dbus.Variant) bool,
) propertyChange {
	if sig == nil || sig.Path != path || sig.Name != signalPropertiesChanged || len(sig.Body) < 2 {
		return propertyUnrelated
	}
	if changedIface, ok := sig.Body[0].(string); !ok || changedIface != iface {
		return propertyUnrelated
	}
	if changed, ok := sig.Body[1].(map[string]dbus.Variant); ok {
		if v, ok := changed[property]; ok {
			if wantFn(v) {
				return propertyWanted
			}
			return propertyOther
		}
	}
	if len(sig.Body) >= 3 {
		if invalidated, ok := sig.Body[2].([]string); ok {
			for _, name := range invalidated {
				if name == property {
					return propertyInvalidated
				}
			}
		}
	}
	return propertyUnrelated
}

// watchProperty waits until wantFn accepts the value of property.
//
// It subscribes to PropertiesChanged first and then reads the current value
// once, so a change that happens between the two cannot be missed, and then
// waits for signals. AddMatch is synchronous, so the subscription is active
// before the read. A slow poll remains as a safety net in case a signal is
// dropped. Everything runs in the calling goroutine.
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

	match := propertiesChangedMatchRule(path)
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return fmt.Errorf("watch %s on %s: subscribe PropertiesChanged: %w", property, path, err)
	}
	defer func() { _ = c.conn.RemoveMatchSignal(match...) }()

	sigCh := make(chan *dbus.Signal, signalChannelBuffer)
	c.conn.Signal(sigCh)
	defer c.conn.RemoveSignal(sigCh)

	check := func() bool {
		v, err := c.getProperty(ctx, path, iface, property)
		if err != nil {
			if c.verbose && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "%sread %s on %s: %v (retrying)\n", stderrPrefix, property, path, err)
			}
			return false
		}
		return wantFn(v)
	}
	if check() {
		return nil
	}

	ticker := time.NewTicker(watchPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("watch %s on %s: %w", property, path, ctx.Err())
		case sig := <-sigCh:
			switch classifyPropertyChange(sig, path, iface, property, wantFn) {
			case propertyWanted:
				return nil
			case propertyInvalidated:
				if check() {
					return nil
				}
			}
		case <-ticker.C:
			if check() {
				return nil
			}
		}
	}
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

	bondCtx, cancel := context.WithTimeout(ctx, c.timeouts.bonding)
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
