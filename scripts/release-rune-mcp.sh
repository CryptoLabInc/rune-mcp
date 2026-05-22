#!/usr/bin/env bash
# Cross-build rune-mcp server
#
# Output layout: dist/rune-mcp-<os>-<arch> + dist/checksums.txt
#
# Usage:
#   scripts/release-rune-mcp.sh [version]

set -euo pipefail

VERSION="${1:-v0.4.0-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/dist"

# TODO: add linux/arm64, darwin/amd64, darwin/arm64 once cross-CGO is available
TARGETS=(
  "linux  amd64  rune-mcp-linux-amd64"
)

rm -rf "$OUT"
mkdir -p "$OUT"

LDFLAGS="-s -w -X main.version=$VERSION"

for line in "${TARGETS[@]}"; do
  read -r GOOS GOARCH ASSET <<<"$line"
  echo "building $ASSET ($GOOS/$GOARCH)..."
  GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$LDFLAGS" \
    -o "$OUT/$ASSET" "$ROOT/cmd/rune-mcp"
done

echo "computing checksums..."
(cd "$OUT" && sha256sum rune-mcp-* > checksums.txt)

echo
echo "release artifacts ready in $OUT/ for $VERSION:"
ls -lh "$OUT"
