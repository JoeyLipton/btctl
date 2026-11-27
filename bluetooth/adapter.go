// Package bluetooth - adapter.go covers local hci adapter operations: listing
// adapters, tracking the active one, reading its status, and toggling Powered
// and Discoverable. Powering on includes a single rfkill-unblock recovery
// attempt, because a soft-blocked adapter is the common reason a Powered=true
// write fails and BlueZ does not unblock rfkill itself.
package bluetooth

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Adapter is a snapshot of a local Bluetooth adapter exposed by BlueZ.
type Adapter struct {
	Path         dbus.ObjectPath
	Name         string // e.g. "hci0"
	Address      string
	Powered      bool
	Discovering  bool
	Discoverable bool
	Alias        string // user-friendly name if set, else same as Name
}

// ListAdapters returns every hci* adapter known to BlueZ, sorted by Name.
func (c *Client) ListAdapters(ctx context.Context) ([]Adapter, error) {
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list adapters: %w", err)
	}

	adapters := make([]Adapter, 0)
	for path, ifaces := range objects {
		props, ok := ifaces[bluezAdapterIface]
		if !ok {
			continue
		}
		name := adapterNameFromPath(path)
		if !strings.HasPrefix(name, "hci") {
			continue
		}
		adapters = append(adapters, parseAdapter(path, props))
	}

	sort.Slice(adapters, func(i, j int) bool {
		return adapters[i].Name < adapters[j].Name
	})
	return adapters, nil
}

// SetActiveAdapter selects the adapter that Scan and adapter control target.
func (c *Client) SetActiveAdapter(path dbus.ObjectPath) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.activeAdapter = path
	c.mu.Unlock()
}

// ActiveAdapter returns the currently selected adapter path, or "" when none
// has been set.
func (c *Client) ActiveAdapter() dbus.ObjectPath {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeAdapter
}

// ActiveAdapterInfo returns the Adapter snapshot for the active adapter.
func (c *Client) ActiveAdapterInfo(ctx context.Context) (Adapter, error) {
	path := c.ActiveAdapter()
	if path == "" {
		return Adapter{}, fmt.Errorf("adapter info: %w", ErrNoAdapter)
	}
	adapters, err := c.ListAdapters(ctx)
	if err != nil {
		return Adapter{}, err
	}
	for _, a := range adapters {
		if a.Path == path {
			return a, nil
		}
	}
	return Adapter{}, fmt.Errorf("adapter info %s: %w", path, ErrAdapterNotReady)
}

// GetAdapterPowered reports whether the active adapter is powered.
func (c *Client) GetAdapterPowered(ctx context.Context) (bool, error) {
	a, err := c.ActiveAdapterInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("get adapter powered: %w", err)
	}
	return a.Powered, nil
}

// SetAdapterPowered sets the Powered property on org.bluez.Adapter1 at path.
func (c *Client) SetAdapterPowered(ctx context.Context, path dbus.ObjectPath, powered bool) error {
	return c.setAdapterPowered(ctx, path, powered, false)
}

func (c *Client) setAdapterPowered(ctx context.Context, path dbus.ObjectPath, powered bool, retried bool) error {
	err := c.setAdapterProperty(ctx, path, propAdapterPowered, powered)
	if err == nil {
		return nil
	}
	if retried || !adapterPoweredNeedsRecovery(err) {
		return fmt.Errorf("set adapter powered %s: %w", path, err)
	}
	if c.verbose {
		fmt.Fprintln(os.Stderr, stderrPrefix+"rfkill unblock attempted")
	}
	if rfErr := c.tryRfkillUnblock(ctx); rfErr != nil {
		return fmt.Errorf("set adapter powered %s: %w", path, err)
	}
	return c.setAdapterPowered(ctx, path, powered, true)
}

// SetDiscoverable sets the Discoverable property on org.bluez.Adapter1 at path.
func (c *Client) SetDiscoverable(ctx context.Context, path dbus.ObjectPath, on bool) error {
	if err := c.setAdapterProperty(ctx, path, propAdapterDiscoverable, on); err != nil {
		return fmt.Errorf("set discoverable %s: %w", path, err)
	}
	if on {
		if err := c.setAdapterProperty(ctx, path, propAdapterDiscoverableTimeout, uint32(0)); err != nil {
			return fmt.Errorf("set discoverable timeout %s: %w", path, err)
		}
	}
	return nil
}

// tryRfkillUnblock runs rfkill unblock bluetooth and waits for Powered=true.
func (c *Client) tryRfkillUnblock(ctx context.Context) error {
	path, err := resolveBinary("rfkill")
	if err != nil {
		return fmt.Errorf("rfkill not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, "unblock", "bluetooth")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			return fmt.Errorf("rfkill unblock bluetooth: %w", runErr)
		}
		return fmt.Errorf("rfkill unblock bluetooth: %w: %s", runErr, text)
	}
	return c.waitForAdapterPowered(ctx)
}

// waitForAdapterPowered polls until the adapter reports powered.
func (c *Client) waitForAdapterPowered(ctx context.Context) error {
	deadline := time.Now().Add(rfkillRecoveryTimeout)
	var lastErr error
	for {
		powered, err := c.GetAdapterPowered(ctx)
		if err == nil && powered {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return fmt.Errorf("wait adapter powered: %w", lastErr)
			}
			return fmt.Errorf("wait adapter powered: timeout after unblock")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait adapter powered: %w", ctx.Err())
		case <-time.After(rfkillRecoveryInterval):
		}
	}
}

// setAdapterProperty writes a single property on org.bluez.Adapter1 at path.
func (c *Client) setAdapterProperty(ctx context.Context, path dbus.ObjectPath, prop string, value interface{}) error {
	if err := c.callCtx(ctx, path, dbusPropertiesIface, "Set", bluezAdapterIface, prop, dbus.MakeVariant(value)); err != nil {
		return err
	}
	return nil
}

// adapterPoweredNeedsRecovery reports whether a Powered-set failure may be
// recoverable via rfkill unblock. Newer BlueZ reports an rfkill block as
// org.bluez.Error.Blocked; older releases use Failed with the reason
// "Blocked through rfkill".
func adapterPoweredNeedsRecovery(err error) bool {
	if isDBusError(err, errNameBlocked, errNameNotReady, errNameNotAvailable) {
		return true
	}
	return isDBusError(err, errNameFailed) &&
		strings.Contains(strings.ToLower(dbusErrorMessage(err)), "rfkill")
}

// parseAdapter extracts an Adapter from a property map.
func parseAdapter(path dbus.ObjectPath, props map[string]dbus.Variant) Adapter {
	name := adapterNameFromPath(path)
	alias := name
	var s string
	if getObjectProperty(props, "Alias", &s) && s != "" {
		alias = s
	}
	address := ""
	getObjectProperty(props, "Address", &address)
	var powered, discovering, discoverable bool
	getObjectProperty(props, propAdapterPowered, &powered)
	getObjectProperty(props, "Discovering", &discovering)
	getObjectProperty(props, propAdapterDiscoverable, &discoverable)
	return Adapter{
		Path:         path,
		Name:         name,
		Address:      address,
		Powered:      powered,
		Discovering:  discovering,
		Discoverable: discoverable,
		Alias:        alias,
	}
}

// adapterNameFromPath returns the last path segment (e.g. "hci0").
func adapterNameFromPath(path dbus.ObjectPath) string {
	s := string(path)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
