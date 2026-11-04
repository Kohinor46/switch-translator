//go:build !((darwin && (arm64 || amd64)) || (linux && (amd64 || arm64)) || (windows && amd64))

package ffmpeg

import "embed"

// для остальных платформ ffmpeg не вшивается
var embedded embed.FS
