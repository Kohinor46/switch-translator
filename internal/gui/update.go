package gui

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	hexenc "encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Version — версия программы. tools/release.sh берёт её отсюда: новый выпуск — новая строка здесь.
const Version = "1.0.0"

const (
	repository       = "Kohinor46/switch-translator"
	releasesPage     = "https://github.com/" + repository + "/releases/latest"
	releasesAPI      = "https://api.github.com/repos/" + repository + "/releases/latest"
	prefCheckUpdates = "checkUpdates"
)

// release — последний выпуск на GitHub.
type release struct {
	Version string
	Notes   string
	Page    string
	Asset   string // архив для этой системы
	SHA256  string // из GitHub («digest»); без него обновление не ставится
}

// assetName — архив выпуска для этой системы.
func assetName() string {
	if runtime.GOOS == "darwin" {
		return "SwitchTranslator-macOS.zip"
	}
	return "SwitchTranslator-Windows.zip"
}

func latestRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SwitchTranslator/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	rel := &release{Version: strings.TrimPrefix(r.Tag, "v"), Notes: r.Body, Page: r.Page}
	if !strings.HasPrefix(rel.Page, "https://github.com/") {
		rel.Page = releasesPage
	}
	for _, a := range r.Assets {
		if a.Name == assetName() {
			rel.Asset = a.URL
			rel.SHA256, _ = strings.CutPrefix(a.Digest, "sha256:")
		}
	}
	return rel, nil
}

// newerVersion — новее ли latest («1.2.0»), чем current.
func newerVersion(current, latest string) bool {
	parse := func(v string) []int {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	c, l := parse(current), parse(latest)
	if c == nil || l == nil {
		return false
	}
	for i := range max(len(c), len(l)) {
		x, y := 0, 0
		if i < len(c) {
			x = c[i]
		}
		if i < len(l) {
			y = l[i]
		}
		if x != y {
			return y > x
		}
	}
	return false
}

// checkUpdate — проверка при запуске (если не выключена): о новой версии — окно с предложением обновиться.
func (u *ui) checkUpdate() {
	cleanupOldExe()
	if !u.prefs.BoolWithFallback(prefCheckUpdates, true) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := latestRelease(ctx)
		if err != nil || !newerVersion(Version, rel.Version) {
			return
		}
		fyne.Do(func() { u.offerUpdate(rel) })
	}()
}

// checkUpdateNow — «Справка → Проверить обновления»: сообщает результат в любом случае.
func (u *ui) checkUpdateNow() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := latestRelease(ctx)
		fyne.Do(func() {
			switch {
			case err != nil:
				u.showError(fmt.Errorf(T("Не удалось проверить обновления: %v"), err))
			case !newerVersion(Version, rel.Version):
				dialog.ShowInformation(T("Обновления"), T("У тебя последняя версия — %s.", Version), u.win)
			default:
				u.offerUpdate(rel)
			}
		})
	}()
}

// offerUpdate — окно «вышла новая версия» с заметками к выпуску.
func (u *ui) offerUpdate(rel *release) {
	notes := widget.NewRichTextFromMarkdown(rel.Notes)
	notes.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(notes)
	scroll.SetMinSize(fyne.NewSize(520, 220))
	head := widget.NewLabel(T("Вышла версия %s (у тебя %s).", rel.Version, Version))
	content := container.NewBorder(head, nil, nil, nil, scroll)
	canInstall := rel.Asset != "" && rel.SHA256 != "" && installTarget() != ""
	confirm := T("Обновить")
	if !canInstall {
		confirm = T("Открыть страницу")
	}
	d := dialog.NewCustomConfirm(T("Обновление"), confirm, T("Позже"), content, func(ok bool) {
		if !ok {
			return
		}
		if !canInstall {
			u.openURL(rel.Page)
			return
		}
		u.installUpdate(rel)
	}, u.win)
	d.Show()
}

func (u *ui) openURL(s string) {
	if p, err := url.Parse(s); err == nil {
		_ = u.app.OpenURL(p)
	}
}

// installUpdate скачивает архив, сверяет sha256, ставит новую версию на место текущей и перезапускает программу.
func (u *ui) installUpdate(rel *release) {
	u.run(T("Обновление"), false, func(ctx context.Context) (func(), error) {
		target := installTarget()
		dir, err := os.MkdirTemp(filepath.Dir(target), ".switch-translator-update-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		zipPath := filepath.Join(dir, "update.zip")
		if err := download(ctx, rel.Asset, zipPath, func(done, total int64) {
			u.progressFn(T("Скачиваю обновление"), float64(done), float64(total))
		}); err != nil {
			return nil, err
		}
		if err := checkSHA256(zipPath, rel.SHA256); err != nil {
			return nil, err
		}
		if err := replaceApp(zipPath, dir, target); err != nil {
			return nil, err
		}
		u.logf("Установлена версия %s, перезапуск", rel.Version)
		return func() { relaunch(u.app) }, nil
	})
}

func download(ctx context.Context, src, dst string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SwitchTranslator/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			progress(done, resp.ContentLength)
		}
		if err == io.EOF {
			return f.Close()
		}
		if err != nil {
			return err
		}
	}
}

func checkSHA256(p, want string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hexenc.EncodeToString(h.Sum(nil)), want) {
		return errors.New(T("Контрольная сумма архива не совпала — обновление не установлено."))
	}
	return nil
}

// installTarget — что заменяет обновление: папка .app на Mac, сам .exe на Windows.
// "" — программа запущена не из собранного приложения (go run, тесты): ставить некуда.
func installTarget() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, _ = filepath.EvalSymlinks(exe)
	switch runtime.GOOS {
	case "darwin":
		app := filepath.Dir(filepath.Dir(filepath.Dir(exe))) // X.app/Contents/MacOS/bin
		if strings.HasSuffix(app, ".app") {
			return app
		}
	case "windows":
		if strings.HasSuffix(strings.ToLower(exe), ".exe") {
			return exe
		}
	}
	return ""
}

// replaceApp распаковывает архив в work и ставит новую версию на место target.
func replaceApp(zipPath, work, target string) error {
	unpacked := filepath.Join(work, "new")
	if runtime.GOOS == "darwin" {
		// ditto сохраняет права и подпись приложения
		if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", zipPath, unpacked).CombinedOutput(); err != nil {
			return fmt.Errorf("ditto: %v: %s", err, out)
		}
		apps, _ := filepath.Glob(filepath.Join(unpacked, "*.app"))
		if len(apps) != 1 {
			return errors.New(T("В архиве обновления нет приложения."))
		}
		old := filepath.Join(work, "old.app")
		if err := os.Rename(target, old); err != nil {
			return err
		}
		if err := os.Rename(apps[0], target); err != nil {
			os.Rename(old, target)
			return err
		}
		return nil
	}
	if err := unzip(zipPath, unpacked); err != nil {
		return err
	}
	exes, _ := filepath.Glob(filepath.Join(unpacked, "*.exe"))
	if len(exes) != 1 {
		return errors.New(T("В архиве обновления нет программы."))
	}
	// запущенный .exe нельзя перезаписать, но можно переименовать; старый удаляется при следующем запуске
	old := target + ".old"
	os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(exes[0], target); err != nil {
		os.Rename(old, target)
		return err
	}
	return nil
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		p := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(p, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("zip: путь %q вне папки", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o200)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanupOldExe удаляет .exe, оставшийся от прошлого обновления на Windows.
func cleanupOldExe() {
	if t := installTarget(); t != "" && runtime.GOOS == "windows" {
		os.Remove(t + ".old")
	}
}

// relaunch запускает программу заново и закрывает текущую.
func relaunch(a fyne.App) {
	target := installTarget()
	switch {
	case runtime.GOOS == "darwin" && target != "":
		// open ждёт выхода этой копии не нужно: -n запускает новый экземпляр
		exec.Command("/bin/sh", "-c", `sleep 1; /usr/bin/open -n "$0"`, target).Start()
	default:
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe, os.Args[1:]...)
			cmd.Start()
		}
	}
	a.Quit()
}
