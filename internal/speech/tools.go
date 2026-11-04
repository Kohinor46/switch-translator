package speech

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// ToolsDir — папка, куда программа сама скачивает вспомогательные программы.
func ToolsDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(home(), ".cache")
	}
	return filepath.Join(dir, "SwitchTranslator", "tools")
}

func vgmstreamExe() string {
	if runtime.GOOS == "windows" {
		return "vgmstream-cli.exe"
	}
	return "vgmstream-cli"
}

// vgmstreamZip — готовые сборки с github.com/vgmstream/vgmstream. Сборка для Mac
// зависит от библиотек Homebrew, поэтому на Mac vgmstream ставится через brew.
func vgmstreamZip() string {
	switch runtime.GOOS {
	case "windows":
		return "vgmstream-win64.zip"
	case "linux":
		return "vgmstream-linux.zip"
	}
	return ""
}

var vgmMu sync.Mutex

// vgmstream находит vgmstream-cli, а под Windows и Linux при необходимости скачивает его.
func vgmstream(ctx context.Context) (string, error) {
	if p, err := findTool("vgmstream-cli"); err == nil {
		return p, nil
	}
	vgmMu.Lock()
	defer vgmMu.Unlock()
	dir := filepath.Join(ToolsDir(), "vgmstream")
	exe := filepath.Join(dir, vgmstreamExe())
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	name := vgmstreamZip()
	if name == "" {
		return "", fmt.Errorf("этот звук не от Nintendo — для него нужен vgmstream: %s", VgmstreamCommand)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://github.com/vgmstream/vgmstream/releases/latest/download/"+name, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("не удалось скачать vgmstream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("не удалось скачать vgmstream: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != f.Name {
			continue // в архиве только файлы верхнего уровня
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(tmp, f.Name), b, 0o755); err != nil {
			return "", err
		}
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return exe, nil
}
