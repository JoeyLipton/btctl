package main

import (
	"context"
	"fmt"

	"github.com/joeylipton/btctl/bluetooth"
	"github.com/joeylipton/btctl/internal/state"
)

func runAdapter(ctx context.Context, client *bluetooth.Client, args []string) (int, error) {
	sub := args[0]
	switch sub {
	case "list":
		return runAdapterList(ctx, client)
	case "status":
		return runAdapterStatus(ctx, client)
	case "power":
		on := args[1] == "on"
		return runAdapterPower(ctx, client, on)
	case "discoverable":
		on := args[1] == "on"
		return runAdapterDiscoverable(ctx, client, on)
	case "select":
		return runAdapterSelect(ctx, client, args[1])
	default:
		return exitUsage, errBadUsage
	}
}

func runAdapterList(ctx context.Context, client *bluetooth.Client) (int, error) {
	adapters, err := client.ListAdapters(ctx)
	if err != nil {
		return exitError, fmt.Errorf("adapter list: %w", err)
	}
	if len(adapters) == 0 {
		fmt.Println("no adapters found")
		return exitSuccess, nil
	}
	active := client.ActiveAdapter()
	for _, a := range adapters {
		status := "off"
		if a.Powered {
			status = "powered"
		}
		line := fmt.Sprintf("%s  %s  %s", a.Name, a.Address, status)
		if a.Path == active {
			line += "  (active)"
		}
		fmt.Println(line)
	}
	return exitSuccess, nil
}

func runAdapterStatus(ctx context.Context, client *bluetooth.Client) (int, error) {
	a, err := client.ActiveAdapterInfo(ctx)
	if err != nil {
		return exitError, fmt.Errorf("adapter status: %w", err)
	}
	fmt.Printf("Name           %s\n", a.Name)
	fmt.Printf("Address        %s\n", a.Address)
	fmt.Printf("Alias          %s\n", a.Alias)
	fmt.Printf("Powered        %s\n", yesNo(a.Powered))
	fmt.Printf("Discovering    %s\n", yesNo(a.Discovering))
	fmt.Printf("Discoverable   %s\n", yesNo(a.Discoverable))
	return exitSuccess, nil
}

func runAdapterPower(ctx context.Context, client *bluetooth.Client, on bool) (int, error) {
	path := client.ActiveAdapter()
	if path == "" {
		return exitError, fmt.Errorf("adapter power: no active adapter")
	}
	if err := client.SetAdapterPowered(ctx, path, on); err != nil {
		return exitError, fmt.Errorf("adapter power: %w", err)
	}
	if on {
		fmt.Println("adapter powered on")
	} else {
		fmt.Println("adapter powered off")
	}
	return exitSuccess, nil
}

func runAdapterDiscoverable(ctx context.Context, client *bluetooth.Client, on bool) (int, error) {
	path := client.ActiveAdapter()
	if path == "" {
		return exitError, fmt.Errorf("adapter discoverable: no active adapter")
	}
	if err := client.SetDiscoverable(ctx, path, on); err != nil {
		return exitError, fmt.Errorf("adapter discoverable: %w", err)
	}
	if on {
		fmt.Println("adapter is now discoverable")
	} else {
		fmt.Println("adapter discoverable mode disabled")
	}
	return exitSuccess, nil
}

func runAdapterSelect(ctx context.Context, client *bluetooth.Client, name string) (int, error) {
	adapters, err := client.ListAdapters(ctx)
	if err != nil {
		return exitError, fmt.Errorf("adapter select: %w", err)
	}
	for _, a := range adapters {
		if a.Name == name {
			client.SelectAdapter(a)
			if err := state.SaveAdapter(a.Name); err != nil {
				return exitError, fmt.Errorf("adapter select: %w", err)
			}
			fmt.Printf("active adapter set to %s (%s)\n", a.Name, a.Address)
			return exitSuccess, nil
		}
	}
	return exitError, fmt.Errorf("adapter select: %w: %s", bluetooth.ErrAdapterNotFound, name)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
