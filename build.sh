#!/bin/bash
# Builds TV Cast for every platform into dist/.
#   ./build.sh            -> all platforms (macOS app needs Xcode Command Line Tools)
#   ./build.sh mac        -> only the macOS app
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-$(cat VERSION)}"
LDFLAGS="-s -w -X main.Version=$VERSION"
OUT=dist; mkdir -p "$OUT" build
export CGO_ENABLED=0

engine() { (cd engine && GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$LDFLAGS ${3:-}" -o "../$4" .); }

build_mac() {
  echo "==> macOS app (universal)"
  engine darwin arm64 "" build/tvcast-engine-arm64
  engine darwin amd64 "" build/tvcast-engine-amd64
  lipo -create -output build/tvcast-engine build/tvcast-engine-arm64 build/tvcast-engine-amd64
  for a in arm64 x86_64; do
    swiftc -O -swift-version 5 -target $a-apple-macos12 macos/TVCast.swift -o build/TVCast-$a \
      -framework Cocoa -framework WebKit
  done
  lipo -create -output build/TVCast build/TVCast-arm64 build/TVCast-x86_64
  APP="$OUT/TV Cast.app"; rm -rf "$APP"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  cp build/TVCast "$APP/Contents/MacOS/TVCast"
  cp build/tvcast-engine macos/lidguard-install.sh macos/lidguard-uninstall.sh macos/AppIcon.icns "$APP/Contents/Resources/"
  sed "s/__VERSION__/$VERSION/g" macos/Info.plist > "$APP/Contents/Info.plist"
  codesign --force --deep --sign - "$APP"
  (cd "$OUT" && rm -f TVCast-macOS.zip && ditto -c -k --keepParent "TV Cast.app" TVCast-macOS.zip)
}

build_others() {
  echo "==> Windows (x64, arm64)"
  engine windows amd64 "-H=windowsgui" "$OUT/TVCast-windows-x64.exe"
  engine windows arm64 "-H=windowsgui" "$OUT/TVCast-windows-arm64.exe"
  echo "==> Linux (x64, arm64)"
  engine linux amd64 "" "$OUT/tvcast-linux-x64"
  engine linux arm64 "" "$OUT/tvcast-linux-arm64"
}

case "${1:-all}" in
  mac) build_mac ;;
  others) build_others ;;
  *) build_mac; build_others ;;
esac
(cd "$OUT" && shasum -a 256 TVCast-* tvcast-* 2>/dev/null > SHA256SUMS.txt || true)
echo "==> done: $(ls "$OUT" | tr '\n' ' ')"
