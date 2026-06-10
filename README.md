# btctl

Minimal Bluetooth control CLI for Linux, written in Go on top of BlueZ over the
system D-Bus. No external runtime dependencies beyond a running `bluetoothd`.

## Requirements

- Linux with BlueZ (`bluetoothd` reachable on the system bus)
- Go 1.18+ to build (the connection lock uses `sync.Mutex.TryLock`)
- A user account that can talk to `org.bluez` on the system bus (typically
  membership in the `bluetooth` / `lp` group, or appropriate Polkit rules)

## Build

From the project root (where `go.mod` lives):

    go build -o btctl .

The binary has no runtime dependencies and can be copied to `~/.local/bin/` or
`/usr/local/bin/`.

## Subcommands

### btctl help

Prints a help page covering every subcommand, the scan options,
example invocations, and the exit-code legend. Mirrors `btctl -h` /
`btctl --help`.

### btctl list

Lists paired devices on the active adapter. Connected devices are shown first;
remaining devices are sorted alphabetically by name. Columns are aligned with
`text/tabwriter`.

Example output:

    $ btctl list
    [connected]  WH-1000XM4        AA:BB:CC:DD:EE:FF
    [paired   ]  Logitech MX Keys  11:22:33:44:55:66

If no paired devices are present, prints `no paired devices found` and exits 0.

### btctl status

Status report intended for status bars. The command always exits 0 and never
writes to stderr (no log noise even when BlueZ is unreachable). Runs in well
under 100ms on a warm system.

btctl status output:

    One device connected:   [BT] 1
                            DeviceName    AA:BB:CC:DD:EE:FF
    Multiple devices:       [BT] 3
                            Device1       AA:BB:CC:DD:EE:FF
                            Device2       BB:CC:DD:EE:FF:00
                            Device3       CC:DD:EE:FF:00:11
    None connected:         [BT] 0
    Adapter off/absent:     [BT] off

Use `btctl status | head -1` for polybar/i3bar.

### btctl scan [-t <seconds>] [--scan-time <seconds>]

Runs a Bluetooth discovery on the first hci adapter for the given duration
(default 10 seconds, must be a positive integer). Devices are streamed as they
are discovered, and a name update via PropertiesChanged is shown on its own
line. After the scan ends, the user is prompted to optionally connect.

Devices that BlueZ already knew about before the scan started are excluded
from the output.

Example session:

    $ btctl scan -t 8
    Scanning for 8 seconds...
    found: (unknown)  AA:BB:CC:11:22:33
    found: Pixel 7  DC:71:96:00:00:01
    updated: WH-1000XM4  AA:BB:CC:11:22:33

    Scan complete. 2 new device(s) found.
    New devices found:
      1) WH-1000XM4           AA:BB:CC:11:22:33
      2) Pixel 7              DC:71:96:00:00:01
    Enter number to connect (or press Enter to skip): 1
    Connected to WH-1000XM4 (AA:BB:CC:11:22:33)

Pressing Enter at the prompt exits 0 without connecting. A non-integer entry
prints `invalid selection: <input>` and a number outside the listed range
prints `selection out of range: <n>`; both exit 1.

### btctl connect <address>

Connects to a known device by hardware address. Address matching is
case-insensitive.

    $ btctl connect AA:BB:CC:DD:EE:FF
    $ btctl connect aa:bb:cc:dd:ee:ff

If the address is not present in the current `GetManagedObjects` snapshot, the
command prints `error: device not found: <address>` to stderr and exits 1.

### btctl disconnect <address>

Disconnects from a device. Same address matching rules as `connect`.

    $ btctl disconnect AA:BB:CC:DD:EE:FF

## i3 keybind for the picker

Add to your i3 config (`~/.config/i3/config`):

    bindsym $mod+b exec --no-startup-id btctl pick

`btctl pick` opens the interactive menu (rofi if available, otherwise dmenu)
and gives full access to connect, disconnect, forget, scan, device
info, and adapter settings without typing any address.

## Troubleshooting

### Verifying the pairing agent during `btctl pick`

To verify the agent is registered while `btctl pick` is open, run:

    busctl tree org.bluez

The path `/btctl/agent` will not appear under `org.bluez` because btctl registers
the agent on its own unique bus name, not under the BlueZ service. To confirm
registration run:

    busctl call org.bluez /org/bluez \
      org.bluez.AgentManager1 RequestDefaultAgent o /btctl/agent

BlueZ will return an error if the agent is already registered as default, which
confirms it is active.
