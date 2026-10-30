#!/bin/sh
# Builds the macOS desktop app: dist/GCC Code.app, universal unless ARCH is set.
#
#   scripts/build-app.sh            # build the .app
#   scripts/build-app.sh --dmg      # also pack it into dist/GCC-Code-<version>.dmg
#   scripts/build-app.sh --install  # also copy it to ~/Applications
#
# SIGN_IDENTITY selects the codesign identity; the default "-" is an ad-hoc
# signature, enough to run on this Mac. Distribution needs a Developer ID
# and notarization.
set -eu
cd "$(dirname "$0")/.."

case "$(uname -s)" in Darwin) ;; *) echo "build-app.sh 只能在 macOS 上运行" >&2; exit 1 ;; esac

VERSION=$(sed -n 's/.*Version *= *"\([^"]*\)".*/\1/p' internal/core/version.go | head -1)
[ -n "$VERSION" ] || VERSION=0.0.0
OUT=${OUT:-dist}
APP="$OUT/GCC Code.app"
BIN="$APP/Contents/MacOS/gcccode"
LDFLAGS="-s -w"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

build() { # arch out
	echo "编译 ${1} ..."
	CGO_ENABLED=1 GOOS=darwin GOARCH="$1" go build -trimpath -ldflags "$LDFLAGS" -o "$2" ./cmd/gcccode 2>&1 | grep -v "duplicate libraries" || true
	[ -x "$2" ]
}

if [ -n "${ARCH:-}" ]; then
	build "$ARCH" "$BIN"
else
	build arm64 "$OUT/gcccode-arm64"
	build amd64 "$OUT/gcccode-amd64"
	lipo -create -output "$BIN" "$OUT/gcccode-arm64" "$OUT/gcccode-amd64"
	rm -f "$OUT/gcccode-arm64" "$OUT/gcccode-amd64"
fi

sed "s/__VERSION__/$VERSION/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
cp packaging/macos/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
printf 'APPL????' > "$APP/Contents/PkgInfo"

codesign --force --deep --sign "${SIGN_IDENTITY:--}" "$APP"
echo "已生成 ${APP} (${VERSION}, $(lipo -archs "$BIN" 2>/dev/null || echo "${ARCH:-}"))"

for arg in "$@"; do
	case "$arg" in
	--dmg)
		DMG="$OUT/GCC-Code-$VERSION.dmg"
		STAGE=$(mktemp -d)
		cp -R "$APP" "$STAGE/"
		ln -s /Applications "$STAGE/Applications"
		rm -f "$DMG"
		hdiutil create -quiet -volname "GCC Code" -srcfolder "$STAGE" -ov -format UDZO "$DMG"
		rm -rf "$STAGE"
		echo "已生成 ${DMG}"
		;;
	--install)
		mkdir -p "${HOME}/Applications"
		rm -rf "${HOME}/Applications/GCC Code.app"
		cp -R "$APP" "${HOME}/Applications/"
		echo "已安装到 ${HOME}/Applications/GCC Code.app"
		;;
	esac
done
