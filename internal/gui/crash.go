package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"fyne.io/fyne/v2"
)

// Если программа всё-таки упадёт (паника, ошибка в C-коде whisper/ggml), Go запишет причину
// в crash.log; при следующем запуске она покажется в логе и в окне — её можно скопировать.

func crashLogPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "SwitchTranslator", "crash.log")
}

// setupCrashLog возвращает текст прошлого падения ("" — его не было) и включает запись нового.
func setupCrashLog() string {
	p := crashLogPath()
	prev, _ := os.ReadFile(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ""
	}
	if len(prev) > 0 {
		os.WriteFile(strings.TrimSuffix(p, ".log")+"-прошлый.log", prev, 0o644)
	}
	f, err := os.Create(p)
	if err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close() // SetCrashOutput держит свою копию дескриптора
	}
	return strings.TrimSpace(string(prev))
}

// reportPrevCrash показывает, почему программа закрылась в прошлый раз.
func (u *ui) reportPrevCrash(text string) {
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	head := strings.Join(lines[:min(len(lines), 40)], "\n")
	u.addLog(T("Прошлый запуск программы аварийно завершился. Полный текст — в файле ") +
		strings.TrimSuffix(crashLogPath(), ".log") + "-прошлый.log")
	u.errorDialog(fmt.Errorf(T("Прошлый запуск программы аварийно завершился. Скопируй этот текст и пришли его — по нему видно, где сломалось:\n\n%s"), head))
}

// recovered превращает панику в ошибку со стеком: задача падает, а программа — нет.
func recovered(r any) error {
	return fmt.Errorf(T("внутренняя ошибка программы: %v\n\n%s"), r, debug.Stack())
}

// goSafe — фоновая работа без риска уронить программу.
func (u *ui) goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := recovered(r)
				fyne.Do(func() { u.showError(err) })
			}
		}()
		fn()
	}()
}
