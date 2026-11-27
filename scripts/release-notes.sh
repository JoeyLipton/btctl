#!/usr/bin/env bash
# Print the CHANGELOG.md section for a version, for use as GitHub release
# notes. Usage: scripts/release-notes.sh v0.1.0
set -euo pipefail

version="${1:?usage: $0 <version> (e.g. v0.1.0)}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

notes="$(awk -v v="${version#v}" '
	/^## \[/ {
		if (found) exit
		if (index($0, "## [" v "]") == 1) { found = 1; next }
	}
	found { print }
' "$root/CHANGELOG.md")"

if [[ -z "${notes//[[:space:]]/}" ]]; then
	echo "no CHANGELOG.md section for ${version#v}" >&2
	exit 1
fi
printf '%s\n' "$notes"
