#!/usr/bin/env bash
# Build release archives of btctl for every supported platform into dist/,
# plus a SHA256SUMS file. The release workflow runs this on every v* tag;
# `make dist` runs it locally.
#
# Usage: scripts/build-release.sh <version>      e.g. scripts/build-release.sh v0.1.0
#
# Builds are static (CGO_ENABLED=0), stripped and path-independent
# (-trimpath), and archives are reproducible: file order, ownership and
# timestamps (taken from the last commit) do not depend on the build machine.
set -euo pipefail

version="${1:?usage: $0 <version> (e.g. v0.1.0)}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="$root/dist"

# GOOS GOARCH GOARM archive-suffix
targets=(
	"linux amd64 - amd64"
	"linux arm64 - arm64"
	"linux arm   7 armv7"
	"linux arm   6 armv6"
)

epoch="${SOURCE_DATE_EPOCH:-$(git -C "$root" log -1 --format=%ct 2>/dev/null || date +%s)}"
ldflags="-s -w -X main.version=${version}"
docs=(README.md LICENSE CHANGELOG.md)

rm -rf "$dist"
mkdir -p "$dist"

for target in "${targets[@]}"; do
	read -r goos goarch goarm suffix <<<"$target"
	name="btctl_${version#v}_${goos}_${suffix}"
	stage="$dist/$name"
	mkdir -p "$stage"

	echo "building $name"
	if [[ "$goarm" == "-" ]]; then
		goarm=""
	fi
	(
		cd "$root"
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
			go build -trimpath -ldflags "$ldflags" -o "$stage/btctl" .
	)
	for doc in "${docs[@]}"; do
		cp "$root/$doc" "$stage/"
	done

	tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
		-C "$dist" -cf - "$name" | gzip -9n >"$dist/$name.tar.gz"
	rm -rf "$stage"
done

(
	cd "$dist"
	sha256sum ./*.tar.gz | sed 's| \./| |' >SHA256SUMS
)
echo "wrote $(find "$dist" -name '*.tar.gz' | wc -l) archives and SHA256SUMS to $dist"
