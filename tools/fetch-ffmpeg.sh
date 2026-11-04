#!/bin/bash
# Скачивает статические сборки ffmpeg и ffprobe и кладёт их сжатыми в internal/ffmpeg/bin/<платформа>,
# откуда они вшиваются в программу при сборке под эту платформу.
#   tools/fetch-ffmpeg.sh                 — для текущей платформы
#   tools/fetch-ffmpeg.sh windows_amd64   — для указанных (darwin_arm64 darwin_amd64 linux_amd64 linux_arm64 windows_amd64)
# Сборки: macOS — ffmpeg.martin-riedl.de (проверяется sha256), Linux и Windows — github.com/BtbN/FFmpeg-Builds.
# Это сборки GPL (с x264 и libvpx); их лицензия и ссылка на исходники — в internal/ffmpeg/LICENSE.md.
set -euo pipefail
cd "$(dirname "$0")/.."
platforms=("$@")
if [ ${#platforms[@]} -eq 0 ]; then
	platforms=("$(go env GOOS)_$(go env GOARCH)")
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
btbn=https://github.com/BtbN/FFmpeg-Builds/releases/download/latest
riedl=https://ffmpeg.martin-riedl.de

for p in "${platforms[@]}"; do
	dst=internal/ffmpeg/bin/$p
	mkdir -p "$dst" "$work/$p"
	echo "== $p"
	case $p in
	darwin_arm64 | darwin_amd64)
		arch=${p#darwin_}
		page=$(curl -fsSL "$riedl/")
		for tool in ffmpeg ffprobe; do
			path=$(grep -oE "/download/macos/$arch/[^\"]*/release/$tool.zip\"|/download/macos/$arch/[^\"]*/$tool.zip\"" <<<"$page" | head -1 | tr -d '"')
			[ -n "$path" ] || { echo "не нашёл $tool для $arch на $riedl"; exit 1; }
			curl -fsSL "$riedl$path" -o "$work/$p/$tool.zip"
			want=$(curl -fsSL "$riedl$path.sha256" | grep -oE '^[0-9a-f]{64}')
			got=$(shasum -a 256 "$work/$p/$tool.zip" | cut -d' ' -f1)
			[ "$want" = "$got" ] || { echo "$tool: sha256 не совпал"; exit 1; }
			unzip -oq "$work/$p/$tool.zip" -d "$work/$p"
		done
		;;
	linux_amd64 | linux_arm64)
		name=linux64
		[ "$p" = linux_arm64 ] && name=linuxarm64
		curl -fsSL "$btbn/ffmpeg-n8.1-latest-$name-gpl-8.1.tar.xz" -o "$work/$p/f.tar.xz"
		tar -xf "$work/$p/f.tar.xz" -C "$work/$p" --strip-components 2 --wildcards '*/bin/ffmpeg' '*/bin/ffprobe' 2>/dev/null ||
			tar -xf "$work/$p/f.tar.xz" -C "$work/$p" --strip-components 2 '*/bin/ffmpeg' '*/bin/ffprobe'
		;;
	windows_amd64)
		curl -fsSL "$btbn/ffmpeg-n8.1-latest-win64-gpl-8.1.zip" -o "$work/$p/f.zip"
		unzip -ojq "$work/$p/f.zip" '*/bin/ffmpeg.exe' '*/bin/ffprobe.exe' -d "$work/$p"
		;;
	*)
		echo "неизвестная платформа $p"
		exit 1
		;;
	esac
	for tool in ffmpeg ffprobe; do
		src=$work/$p/$tool
		[ -f "$src" ] || src=$src.exe
		go run ./tools/zst "$src" "$dst/$tool.zst"
	done
	date "+скачано %Y-%m-%d" >"$dst/VERSION"
	ls -la "$dst"
done
