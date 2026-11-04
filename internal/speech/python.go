package speech

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Локальные нейросетевые голоса (Silero, XTTS) написаны на Python. Программа держит для них своё
// окружение Python в папке инструментов и ставит туда пакеты по кнопке «Установить» — системный
// Python не засоряется. Модель загружается один раз: рядом работает процесс-помощник, программа
// шлёт ему фразы строками JSON и получает ответы, так тысячи реплик не ждут загрузки модели.

// PythonEnv — папка окружения Python для локальных голосов.
func PythonEnv() string { return filepath.Join(ToolsDir(), "python") }

func venvPython() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(PythonEnv(), "Scripts", "python.exe")
	}
	return filepath.Join(PythonEnv(), "bin", "python")
}

// systemPython — Python 3 компьютера, из которого создаётся окружение.
func systemPython() (string, error) {
	// torch и coqui-tts есть не для всех версий: сначала проверенные, потом какой найдётся
	names := []string{"python3.12", "python3.11", "python3.10", "python3.13", "python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "python3", "py"}
	}
	p, err := findTool(names...)
	if err != nil {
		return "", fmt.Errorf("не найден Python 3 — установи его с python.org (версия 3.10–3.12), потом нажми «Установить» ещё раз")
	}
	return p, nil
}

// pythonHas — стоят ли в окружении эти модули.
func pythonHas(modules ...string) bool {
	py := venvPython()
	if _, err := os.Stat(py); err != nil {
		return false
	}
	return exec.Command(py, "-c", "import "+strings.Join(modules, ", ")).Run() == nil
}

// installPython создаёт окружение (если его нет) и ставит пакеты; вывод pip идёт в log построчно.
func installPython(ctx context.Context, pkgs []string, log func(string)) error {
	if _, err := os.Stat(venvPython()); err != nil {
		sys, err := systemPython()
		if err != nil {
			return err
		}
		log("Создаю окружение Python: " + PythonEnv())
		if err := os.MkdirAll(filepath.Dir(PythonEnv()), 0o755); err != nil {
			return err
		}
		if err := runLogged(ctx, log, sys, "-m", "venv", PythonEnv()); err != nil {
			return fmt.Errorf("не удалось создать окружение Python: %w", err)
		}
		runLogged(ctx, log, venvPython(), "-m", "pip", "install", "--upgrade", "pip")
	}
	log("Ставлю " + strings.Join(pkgs, ", ") + " — это может занять несколько минут")
	args := append([]string{"-m", "pip", "install", "--upgrade"}, pkgs...)
	if err := runLogged(ctx, log, venvPython(), args...); err != nil {
		return fmt.Errorf("pip не смог поставить %s: %w", strings.Join(pkgs, ", "), err)
	}
	return nil
}

// runLogged запускает программу и отдаёт её вывод в log построчно; в ошибке — последняя строка.
func runLogged(ctx context.Context, log func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return err
	}
	last := make(chan string)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		l := ""
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				l = t
				log(t)
			}
		}
		last <- l
	}()
	err := cmd.Wait()
	w.Close()
	l := <-last
	if err != nil && l != "" {
		return fmt.Errorf("%v: %s", err, l)
	}
	return err
}

// pyWorker — процесс-помощник с загруженной моделью.
type pyWorker struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Scanner
	stderr *tail
}

// Строки ответа помечены: библиотеки любят печатать своё в stdout.
const pyMark = "@@SWTR "

var (
	workersMu sync.Mutex
	workers   = map[string]*pyWorker{}
)

// pyCall отправляет запрос помощнику key (запускает его со script, если ещё не запущен) и ждёт ответа.
// Помощник выполняет запросы по одному. Отмена ctx завершает его — следующий запрос запустит заново.
func pyCall(ctx context.Context, key, script string, env []string, req any) (map[string]any, error) {
	workersMu.Lock()
	defer workersMu.Unlock()
	w := workers[key]
	if w == nil {
		var err error
		if w, err = startWorker(script, env); err != nil {
			return nil, err
		}
		workers[key] = w
	}
	b, _ := json.Marshal(req)
	type result struct {
		m   map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		if _, err := w.in.Write(append(b, '\n')); err != nil {
			done <- result{nil, err}
			return
		}
		for w.out.Scan() {
			line := w.out.Text()
			if !strings.HasPrefix(line, pyMark) {
				continue
			}
			var m map[string]any
			err := json.Unmarshal([]byte(line[len(pyMark):]), &m)
			done <- result{m, err}
			return
		}
		done <- result{nil, errors.New("помощник Python завершился")}
	}()
	select {
	case <-ctx.Done():
		w.kill()
		delete(workers, key)
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			w.kill()
			delete(workers, key)
			if t := w.stderr.last(); t != "" {
				return nil, fmt.Errorf("%v: %s", r.err, t)
			}
			return nil, r.err
		}
		if e, _ := r.m["error"].(string); e != "" {
			return nil, errors.New(e)
		}
		return r.m, nil
	}
}

// pyPing запускает помощника key и ждёт, пока он загрузит модель. Пакеты иногда забывают указать
// модуль в зависимостях (coqui-tts 0.28 берёт six, который раньше приходил вместе с python-dateutil) —
// тогда модель не загрузится с «No module named 'six'»: такой модуль ставим и пробуем снова.
func pyPing(ctx context.Context, key, script string, env []string, log func(string)) error {
	for try := 0; ; try++ {
		_, err := pyCall(ctx, key, script, env, map[string]string{"op": "ping"})
		mod := missingModule(err)
		if err == nil || mod == "" || try >= 3 {
			return err
		}
		// помощник с незагруженной моделью отвечает только ошибкой — после установки нужен новый
		stopWorker(key)
		log("Не хватает модуля Python " + mod + " — ставлю его")
		if err := installPython(ctx, []string{pipName(mod)}, log); err != nil {
			return err
		}
	}
}

var noModule = regexp.MustCompile(`No module named '([A-Za-z0-9_]+)`)

// missingModule — какого модуля не нашёл Python (верхний уровень: из «yaml.cyaml» — «yaml»).
func missingModule(err error) string {
	if err == nil {
		return ""
	}
	if m := noModule.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// pipName — имя пакета pip для модуля, где они различаются.
func pipName(module string) string {
	switch module {
	case "yaml":
		return "pyyaml"
	case "PIL":
		return "pillow"
	case "sklearn":
		return "scikit-learn"
	case "cv2":
		return "opencv-python"
	case "dateutil":
		return "python-dateutil"
	}
	return module
}

func stopWorker(key string) {
	workersMu.Lock()
	defer workersMu.Unlock()
	if w := workers[key]; w != nil {
		w.kill()
		delete(workers, key)
	}
}

func startWorker(script string, env []string) (*pyWorker, error) {
	cmd := exec.Command(venvPython(), "-u", "-c", script)
	cmd.Env = append(os.Environ(), append([]string{"PYTHONIOENCODING=utf-8"}, env...)...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	w := &pyWorker{cmd: cmd, in: in, stderr: &tail{}}
	cmd.Stderr = w.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 64*1024), 16<<20)
	return w, nil
}

func (w *pyWorker) kill() {
	if w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
	w.cmd.Wait()
}

// StopWorkers завершает помощников Python (при выходе из программы).
func StopWorkers() {
	workersMu.Lock()
	defer workersMu.Unlock()
	for k, w := range workers {
		w.kill()
		delete(workers, k)
	}
}

// tail — последние строки вывода ошибок помощника: для понятного сообщения, если он упал.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 16<<10 {
		t.buf = t.buf[len(t.buf)-8<<10:]
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return lastLine(t.buf)
}
