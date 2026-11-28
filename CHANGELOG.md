# Changelog

All notable changes to btctl are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-11-28

### Fixed

- Release binaries no longer report a "-dirty" source tree in
  `btctl --version` (v0.1.0 shows `dcff7cb40174-dirty`): the release
  workflow wrote its notes file into the checkout before building. The
  release and CI builds now refuse to build from a modified checkout.

### Changed

- Updated github.com/godbus/dbus/v5 from 5.2.2 to 5.2.4.

## [0.1.0] - 2026-11-27

First tagged release, with prebuilt binaries for x86-64 and ARM
(arm64, armv7, armv6).

### Added

- `btctl pair <address>` pairs and trusts a device without connecting it.
- `-a/--adapter hciN` picks the adapter for one run; `btctl adapter select`
  now remembers its choice in `$XDG_STATE_HOME/btctl/adapter`.
- `btctl --version` / `btctl version`.
- Keyboard pairing: the passkey or PIN to type on the device is shown as a
  notification and on stderr; passkey entry works in the menu and, from a
  terminal, on the command line.
- The menu reports outcomes (trust changes, adapter power, errors) as
  desktop notifications when it was opened from a keybinding.
- Ctrl-C and SIGTERM stop discovery, unregister the pairing agent and close
  the picker before exiting with status 130.

### Changed

- `connect` and the `scan` prompt pair a device first when it is not paired
  yet, the same way the menu does, and accept any device BlueZ has seen.
- Device names come from BlueZ's `Alias`, so names set with
  `bluetoothctl set-alias` or GNOME Settings are shown.
- Global flags (`-v`, `-a`) are accepted anywhere on the command line, and
  `btctl -v status` logs like every other command.
- `status`, `list` and `adapter status` read BlueZ's object tree once per
  run instead of up to four times.

### Fixed

- Readable error messages: BlueZ errors were matched by message text that
  godbus never contains, so raw D-Bus errors were shown.
- `connect` no longer gives up after 3 seconds while BlueZ is still
  connecting (slow audio devices).
- `adapter select` had no effect beyond the command itself.
- Pairing confirmations could be lost (a 60 s hang, then failure), and one
  cancelled request broke all later ones.
- `scan` hid in-range devices that BlueZ had cached from an earlier scan,
  and listed devices found on other adapters.
- Notifications could be lost when a command exited right after sending one.
- Device names cut in the middle of a UTF-8 character.
- `hci1` also listed the devices of `hci10`; a rare hang while waiting for a
  property change; leaked background polling after failed pairings and
  already-disconnected devices.
- Powering on a soft-blocked adapter now works: after `rfkill unblock`,
  btctl powers the adapter instead of waiting for it to power itself.

### Security

- btctl registers as BlueZ's default pairing agent only for commands that
  can pair (`connect`, `pair`, `scan`, `pick`), and accepts pairing and
  service-authorization requests only for the device it is currently
  pairing or connecting. Previously any nearby device that started pairing
  while a btctl command ran was accepted.

[Unreleased]: https://github.com/JoeyLipton/btctl/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/JoeyLipton/btctl/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/JoeyLipton/btctl/releases/tag/v0.1.0
