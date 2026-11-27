# Security policy

btctl acts as BlueZ's pairing agent while `connect`, `pair`, `scan` or
`pick` runs, so mistakes in it can let a nearby device pair with your
machine. Reports of such problems are very welcome.

## Reporting a vulnerability

Please report security issues privately through
[GitHub's private vulnerability reporting](https://github.com/JoeyLipton/btctl/security/advisories/new)
rather than in a public issue. Include the btctl version (`btctl --version`),
your BlueZ version, and how to reproduce the problem.

You can expect an acknowledgement within a week. Fixes are released as a new
version and described in [CHANGELOG.md](CHANGELOG.md).

## Supported versions

Security fixes go into the latest release only.
