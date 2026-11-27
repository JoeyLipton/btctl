# btctl

[![CI](https://github.com/JoeyLipton/btctl/actions/workflows/ci.yml/badge.svg)](https://github.com/JoeyLipton/btctl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/JoeyLipton/btctl?sort=semver)](https://github.com/JoeyLipton/btctl/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/JoeyLipton/btctl)](go.mod)
[![License: MIT](https://img.shields.io/github/license/JoeyLipton/btctl)](LICENSE)

A small Bluetooth manager for Linux window-manager setups. btctl talks to
BlueZ over D-Bus and gives you:

- **a rofi/dmenu menu** (`btctl pick`) for connecting, pairing, scanning,
  trust and adapter settings, made to be bound to a key;
- **a status line** (`btctl status`) for polybar, i3bar, waybar and friends;
- **a scriptable CLI** for everything else.

It is a single static binary with no runtime dependencies beyond a running
`bluetoothd`.

## Install

### Prebuilt binaries

Every [release](https://github.com/JoeyLipton/btctl/releases) has static
binaries for `amd64`, `arm64`, `armv7` and `armv6` (Raspberry Pi Zero/1),
plus a `SHA256SUMS` file.

```sh
version=0.1.0
arch=amd64   # or arm64, armv7, armv6
curl -LO "https://github.com/JoeyLipton/btctl/releases/download/v${version}/btctl_${version}_linux_${arch}.tar.gz"
curl -LO "https://github.com/JoeyLipton/btctl/releases/download/v${version}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf "btctl_${version}_linux_${arch}.tar.gz"
sudo install -m755 "btctl_${version}_linux_${arch}/btctl" /usr/local/bin/btctl
```

### With Go

```sh
go install github.com/joeylipton/btctl@latest
```

The module path is lower-case: `github.com/JoeyLipton/btctl@latest` fails
with a module path mismatch.

### From source

```sh
git clone https://github.com/JoeyLipton/btctl.git
cd btctl
make            # gofmt + vet + tests + build
sudo make install   # installs to /usr/local/bin (PREFIX and DESTDIR are honoured)
```

## Requirements

- Linux with BlueZ 5 (`bluetoothd` running on the system bus).
- Permission to talk to `org.bluez` on the system bus: usually being in the
  `bluetooth` (or `lp`) group, or a polkit rule, depending on your distro.
- Optional, for the menu and feedback:
  - `rofi` or `dmenu` for `btctl pick`;
  - `notify-send` (libnotify) for desktop notifications;
  - `zenity` as a fallback scan-progress dialog;
  - `rfkill` (util-linux) so `adapter power on` can lift a soft block.

btctl finds these programs even when it is started from an i3 keybinding or a
polybar click with a stripped `PATH`.

## Usage

```
btctl [-v] [-a <hciN>] <command> [options]
```

| Command | What it does |
| --- | --- |
| `pick [-r\|-d] [-c <file>]` | Open the interactive menu (rofi, or dmenu with `-d`). |
| `list` | List paired devices, connected first, with battery level. |
| `status` | One-line summary for status bars (see below). Always exits 0. |
| `scan [-t <seconds>]` | Discover devices for 1-300 s (default 10), then offer to connect one. |
| `connect <address>` | Connect a device, pairing and trusting it first if needed. |
| `pair <address>` | Pair and trust a device without connecting it. |
| `disconnect <address>` | Disconnect a device. |
| `trust <address>` / `untrust <address>` | Allow or stop automatic reconnection. |
| `forget <address>` | Unpair and remove a device. |
| `adapter list` / `adapter status` | Show the adapters / the active adapter. |
| `adapter power on\|off` | Power the active adapter on or off. |
| `adapter discoverable on\|off` | Make the adapter visible to other devices (no timeout). |
| `adapter select <hciN>` | Choose the active adapter and remember it. |
| `version` | Show the version. |

Global options, accepted anywhere on the command line:

| Option | |
| --- | --- |
| `-v`, `--verbose` | Log every D-Bus call and the pairing agent's traffic to stderr. |
| `-a`, `--adapter <hciN>` | Use this adapter for this run, overriding `adapter select`. |
| `-h`, `--help` | Show the full help page. |
| `--version` | Show the version. |

Addresses are matched case-insensitively. Run `btctl help` for the full help
page.

### Examples

```console
$ btctl list
[connected]  WH-1000XM4        AA:BB:CC:DD:EE:FF  80%
[paired   ]  Logitech MX Keys  11:22:33:44:55:66

$ btctl scan -t 8
Scanning for 8 seconds...
found: Pixel 7  DC:71:96:00:00:01
found: (unknown)  AA:BB:CC:11:22:33
updated: WH-1000XM5  AA:BB:CC:11:22:33

Scan complete. 2 new device(s) found.
New devices found:
  1) Pixel 7              DC:71:96:00:00:01
  2) WH-1000XM5           AA:BB:CC:11:22:33
Enter number to connect (or press Enter to skip): 2
[btctl] pairing with WH-1000XM5 (AA:BB:CC:11:22:33)...
[btctl] connecting to WH-1000XM5 (AA:BB:CC:11:22:33)...
Connected to WH-1000XM5 (AA:BB:CC:11:22:33)

$ btctl -a hci1 connect aa:bb:cc:11:22:33
WH-1000XM5 (AA:BB:CC:11:22:33) is already connected
```

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success (`status` always exits 0) |
| 1 | Runtime error: BlueZ unavailable, device not found, connection failed, ... |
| 2 | Bad command-line arguments |
| 130 | Interrupted with Ctrl-C (discovery is stopped and the agent unregistered first) |

## Pairing

btctl pairs devices it connects to: `connect`, `pair`, the `scan` prompt and
the menu all pair first when needed and mark the device trusted so it
reconnects by itself later.

- **Confirmations** (numeric comparison, "Just Works") are shown as a yes/no
  picker in the menu and accepted automatically on the command line, where
  you asked for that device by address.
- **Keyboards** pair by typing a code on them. btctl shows the code as a
  notification and on stderr.
- **Devices that display a passkey** ask you to enter it: the menu opens a
  text prompt; the command line asks on the terminal.
- **Legacy devices** asking for a PIN get `0000` on the command line (the
  menu offers it as a default).

While a pairing-capable command runs, btctl is BlueZ's default agent. It only
answers requests about the device it is pairing or connecting and rejects
everything else, so a nearby device cannot pair with your machine just
because btctl happens to be running. To pair with a phone or computer, start
the pairing from btctl.

## Desktop integration

### i3 / sway keybinding

```
bindsym $mod+b exec --no-startup-id btctl pick
```

`btctl pick` uses rofi when it is installed, otherwise dmenu. Results and
errors appear as notifications, because nobody sees stderr when the menu is
started from a keybinding.

To style the picker, pass `-c`: for rofi a `.rasi` file, for dmenu a file
with one dmenu argument per line (`#` starts a comment):

```
# ~/.config/btctl/dmenu.args
-fn
monospace:size=11
-l
10
```

```
bindsym $mod+b exec --no-startup-id btctl pick --dmenu -c ~/.config/btctl/dmenu.args
```

### Status bar

`btctl status` prints `[BT] off` when the adapter is off or BlueZ is not
running, otherwise the number of connected devices followed by one line per
device:

```
[BT] 2
WH-1000XM4	AA:BB:CC:DD:EE:FF
MX Master 3	11:22:33:44:55:66
```

It reads BlueZ's state in a single D-Bus call, never writes to stderr
(unless you pass `-v`), and always exits 0, so it is safe to poll every few
seconds. polybar:

```ini
[module/bluetooth]
type = custom/script
exec = btctl status | head -1
interval = 5
click-left = btctl pick
```

Status bars often run commands with a minimal `PATH`; if `btctl` is not
found, use its full path (for example `/usr/local/bin/btctl`).

### Several adapters

Every command uses the active adapter: the one given with `-a`, otherwise
the one saved by `btctl adapter select` (in `$XDG_STATE_HOME/btctl/adapter`,
default `~/.local/state/btctl/adapter`), otherwise the first `hci*` adapter.
A saved adapter that has been unplugged is skipped; an adapter given with
`-a` must exist.

## Troubleshooting

- **"permission denied talking to BlueZ"**: your user may not use
  `org.bluez` on the system bus. Add yourself to the group your distro's
  BlueZ D-Bus policy allows (often `bluetooth` or `lp`) and log in again.
- **"BlueZ is not running"**: start it with `systemctl start bluetooth`.
- **"adapter is blocked by rfkill"**: `btctl adapter power on` unblocks it
  when `rfkill` is installed; otherwise run `rfkill unblock bluetooth`.
- **A device is not found by `connect`**: btctl only knows devices BlueZ has
  seen. Put the device in pairing mode and run `btctl scan` first.
- **See what btctl is doing**: add `-v` to any command. It logs every D-Bus
  call, including the agent registration (`AgentManager1.RegisterAgent`) and
  each request BlueZ sends to the agent.

## Development

```sh
make test        # go test -race ./...
make lint        # gofmt, go vet, staticcheck (if installed)
make dist        # release archives for all platforms in dist/
```

The tests run btctl against a fake `bluetoothd` (`internal/fakebluez`) on a
private `dbus-daemon`, so they need `dbus-daemon` installed (Debian/Ubuntu:
`dbus`) but no Bluetooth hardware, and they never touch the system bus or
your desktop. Without `dbus-daemon` those tests are skipped; set
`BTCTL_REQUIRE_DBUS=1` to make that an error, as CI does.

Code layout:

| Path | |
| --- | --- |
| `*.go` | The command line: argument parsing and commands. |
| `internal/bluez` | BlueZ D-Bus client and pairing agent. No user interface. |
| `internal/menu` | The rofi/dmenu menu behind `btctl pick`. |
| `internal/desktop` | Notifications and scan progress. |
| `internal/state` | The remembered adapter. |
| `internal/fakebluez` | The fake `bluetoothd` used by the tests. |

See [CONTRIBUTING.md](CONTRIBUTING.md) for pull requests and the release
process, and [SECURITY.md](SECURITY.md) for reporting security problems.

## License

[MIT](LICENSE)
