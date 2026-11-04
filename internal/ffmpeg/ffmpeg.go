// Package ffmpeg — ffmpeg и ffprobe, вшитые в программу. Сжатые zstd файлы лежат в bin/<ОС>_<архитектура>
// (их кладёт tools/fetch-ffmpeg.sh) и при первом обращении распаковываются в папку кэша.
package ffmpeg

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ErrMissing — в эту сборку программы ffmpeg не вшит.
var ErrMissing = errors.New("в эту сборку программы ffmpeg не вшит: перед сборкой запусти tools/fetch-ffmpeg.sh")

// binFS — вшитые файлы; у каждой платформы свой embed_<ОС>_<арх>.go.
var binFS fs.FS = embedded

var (
	mu    sync.Mutex
	paths = map[string]string{}
)

// Path — путь к ffmpeg.
func Path() (string, error) { return tool("ffmpeg") }

// ProbePath — путь к ffprobe.
func ProbePath() (string, error) { return tool("ffprobe") }

// Available — вшит ли ffmpeg в эту сборку.
func Available() bool {
	_, err := fs.Stat(binFS, platformDir()+"/ffmpeg.zst")
	return err == nil
}

// Command — готовая команда ffmpeg с аргументами.
func Command(args ...string) (*exec.Cmd, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return exec.Command(p, append([]string{"-hide_banner", "-nostdin"}, args...)...), nil
}

func platformDir() string { return "bin/" + runtime.GOOS + "_" + runtime.GOARCH }

func tool(name string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := paths[name]; ok {
		return p, nil
	}
	packed, err := fs.ReadFile(binFS, platformDir()+"/"+name+".zst")
	if err != nil {
		return "", ErrMissing
	}
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	sum := sha256.Sum256(packed)
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "SwitchTranslator", "ffmpeg", fmt.Sprintf("%x", sum[:6]))
	dst := filepath.Join(dir, exe)
	if st, err := os.Stat(dst); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		paths[name] = dst
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dec, err := zstd.NewReader(bytes.NewReader(packed))
	if err != nil {
		return "", err
	}
	defer dec.Close()
	tmp, err := os.CreateTemp(dir, exe+".*")
	if err != nil {
		return "", err
	}
	_, err = dec.WriteTo(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("распаковка %s: %w", name, err)
	}
	paths[name] = dst
	return dst, nil
}
