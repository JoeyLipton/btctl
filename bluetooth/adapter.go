// Package bluetooth - adapter.go covers local hci adapter operations: listing
// adapters, tracking the active one, reading its status, and toggling Powered
// and Discoverable. Powering on includes a single rfkill-unblock recovery
// attempt, because a soft-blocked adapter is the common reason a Powered=true
// write fails and BlueZ neither unblocks rfkill nor powers the adapter after
// an unblock by itself.
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
	objects, _, err := c.snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("list adapters: %w", err)
	}
	return adaptersIn(objects), nil
}

// adaptersIn extracts the hci* adapters from an object snapshot, sorted by Name.
func adaptersIn(objects managedObjects) []Adapter {
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
	return adapters
}

// SelectAdapter makes a the adapter every later operation on this client
// targets. Persisting the choice across runs is the caller's job.
func (c *Client) SelectAdapter(a Adapter) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.adapterName = a.Name
	c.strictAdapter = true
	c.activeAdapter = a.Path
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
	objects, path, err := c.snapshot(ctx)
	if err != nil {
		return Adapter{}, fmt.Errorf("adapter info: %w", err)
	}
	if path == "" {
		return Adapter{}, fmt.Errorf("adapter info: %w", ErrNoAdapter)
	}
	return parseAdapter(path, objects[path][bluezAdapterIface]), nil
}

// SetAdapterPowered powers the active adapter on or off.
func (c *Client) SetAdapterPowered(ctx context.Context, powered bool) error {
	path, _, err := c.findAdapterPath(ctx)
	if err != nil {
		return fmt.Errorf("set adapter powered: %w", err)
	}
	return c.setAdapterPowered(ctx, path, powered)
}

func (c *Client) setAdapterPowered(ctx context.Context, path dbus.ObjectPath, powered bool) error {
	err := c.setAdapterProperty(ctx, path, propAdapterPowered, powered)
	if err == nil {
		return nil
	}
	if !powered || !adapterPoweredNeedsRecovery(err) {
		return fmt.Errorf("set adapter powered %s: %w", path, err)
	}
	if c.verbose {
		fmt.Fprintln(os.Stderr, stderrPrefix+"adapter is blocked; running rfkill unblock bluetooth")
	}
	if rfErr := c.rfkillUnblock(ctx); rfErr != nil {
		return fmt.Errorf("set adapter powered %s: %w (rfkill unblock failed: %v)", path, err, rfErr)
	}
	// Unblocking does not power the adapter, and the kernel and BlueZ need a
	// moment before it accepts Powered=true, so retry briefly.
	deadline := time.Now().Add(rfkillRecoveryTimeout)
	for {
		err = c.setAdapterProperty(ctx, path, propAdapterPowered, true)
		if err == nil {
			return nil
		}
		if !adapterPoweredNeedsRecovery(err) || time.Now().After(deadline) {
			return fmt.Errorf("set adapter powered %s after rfkill unblock: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("set adapter powered %s: %w", path, ctx.Err())
		case <-time.After(rfkillRecoveryInterval):
		}
	}
}

// SetDiscoverable makes the active adapter visible to other devices (with no
// timeout) or hides it again.
func (c *Client) SetDiscoverable(ctx context.Context, on bool) error {
	path, _, err := c.findAdapterPath(ctx)
	if err != nil {
		return fmt.Errorf("set discoverable: %w", err)
	}
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

// rfkillUnblock runs `rfkill unblock bluetooth`.
func (c *Client) rfkillUnblock(ctx context.Context) error {
	path, err := resolveBinary("rfkill")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, "unblock", "bluetooth")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return fmt.Errorf("rfkill unblock bluetooth: %w: %s", runErr, text)
		}
		return fmt.Errorf("rfkill unblock bluetooth: %w", runErr)
	}
	return nil
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
