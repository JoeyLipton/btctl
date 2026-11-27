// Package menu - adapter.go implements the adapter settings sub-menu.
package menu

import (
	"context"
	"fmt"
	"strings"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/state"
	"github.com/joeylipton/btctl/internal/textfmt"
)

// adapterMenuEntries are the options of the adapter settings sub-menu.
var adapterMenuEntries = []string{
	"[1] Select active adapter",
	"[2] View adapter status",
	"[3] Power on adapter",
	"[4] Power off adapter",
	"[5] Enable discoverable mode",
	"[6] Disable discoverable mode",
}

// menuAdapter drives the adapter settings sub-menu until the user goes back.
func (m *Menu) menuAdapter(ctx context.Context) error {
	for ctx.Err() == nil {
		choice, err := m.prompt(ctx, "Adapter settings", adapterMenuEntries)
		if err != nil || choice == "" {
			return err
		}
		switch {
		case strings.HasPrefix(choice, "[1]"):
			err = m.menuAdapterSelect(ctx)
		case strings.HasPrefix(choice, "[2]"):
			err = m.menuAdapterStatus(ctx)
		case strings.HasPrefix(choice, "[3]"):
			m.setPowered(ctx, true)
		case strings.HasPrefix(choice, "[4]"):
			m.setPowered(ctx, false)
		case strings.HasPrefix(choice, "[5]"):
			m.setDiscoverable(ctx, true)
		case strings.HasPrefix(choice, "[6]"):
			m.setDiscoverable(ctx, false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// menuAdapterSelect chooses the active adapter and remembers it.
func (m *Menu) menuAdapterSelect(ctx context.Context) error {
	adapters, err := m.client.ListAdapters(ctx)
	if err != nil {
		return fmt.Errorf("select adapter: %w", err)
	}
	switch len(adapters) {
	case 0:
		m.tell("No Bluetooth adapters found")
		return nil
	case 1:
		a := adapters[0]
		_, err := m.prompt(ctx, "Adapter settings", []string{fmt.Sprintf("Only one adapter found: %s (%s)", a.Name, a.Address)})
		return err
	}

	active := m.client.ActiveAdapter()
	lines := make([]string, len(adapters))
	for i, a := range adapters {
		lines[i] = adapterLine(a, a.Path == active)
	}
	choice, err := m.prompt(ctx, "Adapter settings: select adapter", lines)
	if err != nil || choice == "" {
		return err
	}
	selected, ok := adapterForLine(adapters, choice)
	if !ok {
		return nil
	}
	m.client.SelectAdapter(selected)
	if err := state.SaveAdapter(selected.Name); err != nil {
		m.tell("Active adapter set to %s (%s) for this session; could not remember it: %s",
			selected.Name, selected.Address, err)
		return nil
	}
	m.tell("Active adapter set to %s (%s)", selected.Name, selected.Address)
	return nil
}

func (m *Menu) menuAdapterStatus(ctx context.Context) error {
	a, err := m.client.ActiveAdapterInfo(ctx)
	if err != nil {
		m.tell("Could not read the adapter: %s", bluez.FormatUserError(err))
		return nil
	}
	lines := []string{
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Name", a.Name),
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Address", a.Address),
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Alias", a.Alias),
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Powered", textfmt.YesNo(a.Powered)),
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Discovering", textfmt.YesNo(a.Discovering)),
		fmt.Sprintf("%-*s %s", adapterLabelWidth, "Discoverable", textfmt.YesNo(a.Discoverable)),
	}
	_, err = m.prompt(ctx, "Adapter status", lines)
	return err
}

func (m *Menu) setPowered(ctx context.Context, on bool) {
	if err := m.client.SetAdapterPowered(ctx, on); err != nil {
		m.tell("Could not change adapter power: %s", bluez.FormatUserError(err))
	} else if on {
		m.tell("Adapter powered on")
	} else {
		m.tell("Adapter powered off")
	}
}

func (m *Menu) setDiscoverable(ctx context.Context, on bool) {
	if err := m.client.SetDiscoverable(ctx, on); err != nil {
		m.tell("Could not change discoverable mode: %s", bluez.FormatUserError(err))
	} else if on {
		m.tell("Adapter is now discoverable")
	} else {
		m.tell("Discoverable mode disabled")
	}
}

// adapterLine formats one adapter as a fixed-width picker line.
func adapterLine(a bluez.Adapter, active bool) string {
	status := "[off    ]"
	if a.Powered {
		status = "[powered]"
	}
	line := fmt.Sprintf("%-*s  %-*s  %s", adapterNameWidth, textfmt.Truncate(a.Name, adapterNameWidth), adapterAddrWidth, a.Address, status)
	if active {
		line += "  [default]"
	}
	return line
}

// adapterForLine maps a picker line back to an adapter by hci name or address.
func adapterForLine(adapters []bluez.Adapter, line string) (bluez.Adapter, bool) {
	fields := strings.Fields(line)
	for _, a := range adapters {
		if len(fields) > 0 && a.Name == fields[0] {
			return a, true
		}
		if len(fields) > 1 && strings.EqualFold(a.Address, fields[1]) {
			return a, true
		}
	}
	return bluez.Adapter{}, false
}
