#!/bin/bash
# Собирает выпуск на macOS:
#   dist/Switch Translator.app            — универсальная (Apple Silicon + Intel), macOS 12+
#   dist/Switch Translator.exe            — Windows x64
#   dist/SwitchTranslator-macOS.zip/.dmg, dist/SwitchTranslator-Windows.zip — для GitHub и обновлений
#   tools/release.sh publish              — то же и выпуск v<версия> на GitHub (нужен gh)
# Версия — из internal/gui/update.go (const Version).
# Нужны: Xcode CLT, Go, fyne CLI (go install fyne.io/tools/cmd/fyne@latest), mingw-w64 для Windows,
# ffmpeg каждой платформы: tools/fetch-ffmpeg.sh darwin_arm64 darwin_amd64 windows_amd64.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
NAME="Switch Translator"
ID=com.switchtranslator.app
VERSION=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' internal/gui/update.go)
REPO=$(sed -n 's/^\trepository *= "\(.*\)"$/\1/p' internal/gui/update.go)
MACOS_MIN=12.0
WORK=$ROOT/.build
DIST=$ROOT/dist
mkdir -p "$WORK" "$DIST"
rm -rf "$DIST"/*
# флаги AVX2/FMA для whisper.cpp под x86_64: при сборке не на x86_64 cgo пропускает их только явно
export CGO_CFLAGS_ALLOW='-mavx2|-mfma|-mf16c' CGO_CXXFLAGS_ALLOW='-mavx2|-mfma|-mf16c'

for p in darwin_arm64 darwin_amd64 windows_amd64; do
	[ -f "internal/ffmpeg/bin/$p/ffmpeg.zst" ] || { echo "нет ffmpeg для $p: tools/fetch-ffmpeg.sh $p"; exit 1; }
done

echo "==> macOS $VERSION"
for arch in arm64 amd64; do
	carch=$([ $arch = arm64 ] && echo arm64 || echo x86_64)
	env CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $carch" CXX="clang++ -arch $carch" \
		MACOSX_DEPLOYMENT_TARGET=$MACOS_MIN CGO_CFLAGS="-mmacosx-version-min=$MACOS_MIN" \
		CGO_CXXFLAGS="-mmacosx-version-min=$MACOS_MIN" CGO_LDFLAGS="-mmacosx-version-min=$MACOS_MIN" \
		go build -trimpath -ldflags="-s -w" -o "$WORK/st-darwin-$arch" ./cmd/switch-translator
done
lipo -create "$WORK/st-darwin-arm64" "$WORK/st-darwin-amd64" -output "$WORK/switch-translator"
fyne package --os darwin --exe "$WORK/switch-translator" --name "$NAME" --app-id $ID \
	--icon "$ROOT/Icon.png" --app-version "$VERSION" --release
mv "$ROOT/$NAME.app" "$DIST/"
PLIST="$DIST/$NAME.app/Contents/Info.plist"
plutil -replace LSMinimumSystemVersion -string $MACOS_MIN "$PLIST"
# своя озвучка пишется с микрофона (ffmpeg через AVFoundation): без этой строки macOS не даст доступ
plutil -replace NSMicrophoneUsageDescription -string "Switch Translator records your own voice-over for game lines." "$PLIST"
codesign --force --deep -s - "$DIST/$NAME.app"
(cd "$DIST" && ditto -c -k --keepParent "$NAME.app" SwitchTranslator-macOS.zip)
STAGE=$WORK/dmg
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp -R "$DIST/$NAME.app" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "$NAME" -srcfolder "$STAGE" -ov -format UDZO "$DIST/SwitchTranslator-macOS.dmg" >/dev/null
rm -rf "$STAGE"

echo "==> Windows $VERSION"
# libstdc++, libgcc и winpthread вшиваются, чтобы .exe не просил DLL MinGW
env CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
	CGO_LDFLAGS="-static -static-libgcc -static-libstdc++" \
	fyne package --os windows --src cmd/switch-translator --name "$NAME" --app-id $ID \
	--icon "$ROOT/Icon.png" --app-version "$VERSION" --release
mv "cmd/switch-translator/$NAME.exe" "$DIST/" 2>/dev/null || mv "$ROOT/$NAME.exe" "$DIST/"
(cd "$DIST" && zip -q SwitchTranslator-Windows.zip "$NAME.exe")

echo "==> Готово"
ls -la "$DIST"

if [ "${1:-}" = publish ]; then
	NOTES=(--generate-notes)
	[ -n "${NOTES_FILE:-}" ] && NOTES=(--notes-file "$NOTES_FILE")
	gh release create "v$VERSION" "$DIST/SwitchTranslator-macOS.zip" "$DIST/SwitchTranslator-macOS.dmg" \
		"$DIST/SwitchTranslator-Windows.zip" --repo "$REPO" --title "Switch Translator $VERSION" "${NOTES[@]}"
fi
