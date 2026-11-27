# Contributing to btctl

Thanks for helping. Bug reports, ideas and pull requests are all welcome.

## Setting up

You need Go (the version in `go.mod` or newer) and `dbus-daemon`
(Debian/Ubuntu: `dbus`). No Bluetooth hardware is needed.

```sh
make test   # go test -race ./...
make lint   # gofmt, go vet, staticcheck (if installed)
make build  # ./btctl
```

The tests run btctl against a fake `bluetoothd` (`internal/fakebluez`) on a
private bus, and stub out `notify-send`, `rofi`, `zenity` and friends, so they
never touch your real Bluetooth setup or desktop.

## Pull requests

- Keep a change focused on one thing, and explain what it fixes or adds.
- Add a test. Bugs in D-Bus code can almost always be reproduced against
  `internal/fakebluez`; extend the fake if it lacks something.
- Match BlueZ errors by D-Bus error name (`isDBusError`), never by message
  text.
- Note user-visible changes under "Unreleased" in `CHANGELOG.md`.

## Releasing

1. Move the "Unreleased" notes in `CHANGELOG.md` under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading and update the links at the bottom.
2. Merge that to `main`, then tag it: `git tag -a vX.Y.Z -m "btctl vX.Y.Z"`
   and `git push origin vX.Y.Z`.
3. The release workflow tests, builds the archives for every platform,
   attests their provenance and publishes the GitHub release with the
   CHANGELOG notes.
