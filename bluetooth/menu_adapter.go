// Package bluetooth - menu_adapter.go implements the adapter settings sub-menu.
package bluetooth

import (
	"context"
	"fmt"
	"strings"

	"github.com/joeylipton/btctl/internal/state"
	"github.com/joeylipton/btctl/internal/textfmt"
)

// adapterMenuEntries are the fixed options in the adapter settings sub-menu.
var adapterMenuEntries = []string{
	"[1] Select active adapter",
	"[2] View adapter status",
	"[3] Power on adapter",
	"[4] Power off adapter",
	"[5] Enable discoverable mode",
	"[6] Disable discoverable mode",
}

// menuAdapter drives the adapter settings sub-menu. Each action returns
// to this menu unless the user cancels back to the main menu.
func (m *Menu) menuAdapter() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		choice, err := m.prompt("Adapter settings", adapterMenuEntries)
		if err != nil {
			return fmt.Errorf("menu adapter: prompt: %w", err)
		}
		if choice == "" {
			return nil
		}
		switch {
		case strings.HasPrefix(choice, "[1]"):
			if err := m.menuAdapterSelect(ctx); err != nil {
				return err
			}
		case strings.HasPrefix(choice, "[2]"):
			if err := m.menuAdapterStatus(ctx); err != nil {
				return err
			}
		case strings.HasPrefix(choice, "[3]"):
			m.menuAdapterSetPowered(ctx, true)
		case strings.HasPrefix(choice, "[4]"):
			m.menuAdapterSetPowered(ctx, false)
		case strings.HasPrefix(choice, "[5]"):
			m.menuAdapterSetDiscoverable(ctx, true)
		case strings.HasPrefix(choice, "[6]"):
			m.menuAdapterSetDiscoverable(ctx, false)
		default:
			continue
		}
	}
}

func (m *Menu) menuAdapterSelect(ctx context.Context) error {
	adapters, err := m.client.ListAdapters(ctx)
	if err != nil {
		return fmt.Errorf("menu adapter select: list adapters: %w", err)
	}
	if len(adapters) == 0 {
		tell("No Bluetooth adapters found")
		return nil
	}
	if len(adapters) == 1 {
		a := adapters[0]
		msg := fmt.Sprintf("Only one adapter found: %s (%s)", a.Name, a.Address)
		_, _ = m.prompt("Adapter settings", []string{msg})
		return nil
	}

	active := m.client.ActiveAdapter()
	lines := make([]string, len(adapters))
	for i, a := range adapters {
		lines[i] = buildAdapterLine(a, a.Path == active)
	}
	choice, err := m.prompt("Adapter settings: select adapter", lines)
	if err != nil {
		return fmt.Errorf("menu adapter select: prompt: %w", err)
	}
	if choice == "" {
		return nil
	}
	selected := matchAdapterByLine(adapters, choice)
	if selected == nil {
		return nil
	}
	m.client.SelectAdapter(*selected)
	if err := state.SaveAdapter(selected.Name); err != nil {
		tell("Active adapter set to %s (%s) for this session; could not remember it: %s",
			selected.Name, selected.Address, err)
		return nil
	}
	tell("Active adapter set to %s (%s)", selected.Name, selected.Address)
	return nil
}

func (m *Menu) menuAdapterStatus(ctx context.Context) error {
	a, err := m.client.ActiveAdapterInfo(ctx)
	if err != nil {
		tell("Could not read the adapter: %s", FormatUserError(err))
		return nil
	}
	infoLines := []string{
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Name", a.Name),
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Address", a.Address),
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Alias", a.Alias),
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Powered", yesNo(a.Powered)),
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Discovering", yesNo(a.Discovering)),
		fmt.Sprintf("%-*s %s", adapterStatusLabelWidth, "Discoverable", yesNo(a.Discoverable)),
	}
	_, _ = m.prompt("Adapter status", infoLines)
	return nil
}

func (m *Menu) menuAdapterSetPowered(ctx context.Context, on bool) {
	path := m.client.ActiveAdapter()
	if path == "" {
		tell("No Bluetooth adapter found")
		return
	}
	if err := m.client.SetAdapterPowered(ctx, path, on); err != nil {
		tell("Could not change adapter power: %s", FormatUserError(err))
		return
	}
	if on {
		tell("Adapter powered on")
	} else {
		tell("Adapter powered off")
	}
}

func (m *Menu) menuAdapterSetDiscoverable(ctx context.Context, on bool) {
	path := m.client.ActiveAdapter()
	if path == "" {
		tell("No Bluetooth adapter found")
		return
	}
	if err := m.client.SetDiscoverable(ctx, path, on); err != nil {
		tell("Could not change discoverable mode: %s", FormatUserError(err))
		return
	}
	if on {
		tell("Adapter is now discoverable")
	} else {
		tell("Discoverable mode disabled")
	}
}

// buildAdapterLine formats one adapter as a fixed-width picker line. Adapters
// are not Device values so buildDeviceLine does not apply; this function is
// the adapter equivalent.
func buildAdapterLine(a Adapter, active bool) string {
	name := a.Name
	name = textfmt.Truncate(name, adapterNameWidth)
	status := "[off    ]"
	if a.Powered {
		status = "[powered]"
	}
	line := fmt.Sprintf("%-*s  %-*s  %s", adapterNameWidth, name, adapterAddrWidth, a.Address, status)
	if active {
		line += "  [default]"
	}
	return line
}

// matchAdapterByLine maps a picker line back to an Adapter by hci name or
// MAC address.
func matchAdapterByLine(adapters []Adapter, line string) *Adapter {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	for i := range adapters {
		if adapters[i].Name == fields[0] {
			return &adapters[i]
		}
	}
	if len(fields) >= 2 {
		addr := strings.ToLower(fields[1])
		for i := range adapters {
			if strings.ToLower(adapters[i].Address) == addr {
				return &adapters[i]
			}
		}
	}
	return nil
}
