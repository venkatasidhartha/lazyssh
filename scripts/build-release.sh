#!/usr/bin/env bash
# Builds static lazyssh binaries for Linux into dist/ together with
# SHA256SUMS and a VERSION file. install.sh downloads from dist/.
#
#   scripts/build-release.sh            # version from git (tag or commit)
#   VERSION=v1.2.0 scripts/build-release.sh
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
TARGETS=(linux/amd64 linux/arm64 linux/arm/7 linux/386)
OUT=dist

rm -rf "$OUT"
mkdir -p "$OUT"

for target in "${TARGETS[@]}"; do
	IFS=/ read -r os arch arm <<<"$target"
	name="lazyssh-${os}-${arch}${arm:+v$arm}"
	echo "building $name ($VERSION)"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM="$arm" \
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
		-o "$OUT/$name" ./cmd/lazyssh
done

echo "$VERSION" >"$OUT/VERSION"
(cd "$OUT" && sha256sum lazyssh-* >SHA256SUMS)
echo
cat "$OUT/SHA256SUMS"
echo "version: $VERSION"
