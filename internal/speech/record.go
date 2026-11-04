package speech

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"switch-translator/internal/ffmpeg"
)

// Запись своей озвучки с микрофона — встроенным ffmpeg: на Mac через AVFoundation,
// на Windows через DirectShow, на Linux через PulseAudio (или ALSA). Пишется WAV 48 кГц моно.
// На macOS при первой записи система спросит разрешение на микрофон.

// Recording — идущая запись.
type Recording struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	out     string
	began   time.Time
	done    chan struct{}
	err     error
	stderr  bytes.Buffer
	stopped sync.Once
}

// StartRecording начинает запись в out (WAV).
func StartRecording(out string) (*Recording, error) {
	if !ffmpeg.Available() {
		return nil, fmt.Errorf("для записи нужен встроенный ffmpeg, а в этой сборке его нет")
	}
	input, err := micInput()
	if err != nil {
		return nil, err
	}
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, input...)
	args = append(args, "-ac", "1", "-ar", "48000", "-c:a", "pcm_s16le", out)
	cmd, err := ffmpeg.Command(args...)
	if err != nil {
		return nil, err
	}
	r := &Recording{cmd: cmd, out: out, done: make(chan struct{})}
	cmd.Stderr = &r.stderr
	if r.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.began = time.Now()
	go func() {
		r.err = cmd.Wait()
		close(r.done)
	}()
	// ffmpeg, который не смог открыть микрофон, выходит сразу — сообщаем об этом тут же
	select {
	case <-r.done:
		return nil, r.failure()
	case <-time.After(400 * time.Millisecond):
	}
	return r, nil
}

// Elapsed — сколько уже записано.
func (r *Recording) Elapsed() time.Duration { return time.Since(r.began) }

// Running — запись ещё идёт (ffmpeg не вышел сам).
func (r *Recording) Running() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// Stop заканчивает запись: ffmpeg получает «q» и дописывает файл как следует.
func (r *Recording) Stop() error {
	r.stopped.Do(func() {
		io.WriteString(r.stdin, "q")
		r.stdin.Close()
	})
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
		<-r.done
	}
	if st, err := os.Stat(r.out); err != nil || st.Size() <= 44 {
		return r.failure()
	}
	return nil
}

// Cancel обрывает запись и удаляет файл.
func (r *Recording) Cancel() {
	r.cmd.Process.Kill()
	<-r.done
	os.Remove(r.out)
}

func (r *Recording) failure() error {
	msg := strings.TrimSpace(r.stderr.String())
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = msg[i+1:]
	}
	hint := ""
	if runtime.GOOS == "darwin" {
		hint = "\nЕсли macOS не спросила разрешение: Системные настройки → Конфиденциальность и безопасность → Микрофон — " +
			"включи программу (или Терминал, если запускаешь из него)."
	}
	if msg == "" {
		msg = "микрофон не записал звук"
	}
	return fmt.Errorf("запись с микрофона не удалась: %s%s", msg, hint)
}

// micInput — аргументы ffmpeg для микрофона по умолчанию.
func micInput() ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return []string{"-f", "avfoundation", "-i", ":default"}, nil
	case "windows":
		name, err := dshowMic()
		if err != nil {
			return nil, err
		}
		return []string{"-f", "dshow", "-i", "audio=" + name}, nil
	default:
		if _, err := exec.LookPath("pactl"); err == nil {
			return []string{"-f", "pulse", "-i", "default"}, nil
		}
		return []string{"-f", "alsa", "-i", "default"}, nil
	}
}

var dshowAudioRe = regexp.MustCompile(`"([^"]+)"\s+\(audio\)`)

// dshowMic — первый микрофон в списке устройств DirectShow.
func dshowMic() (string, error) {
	cmd, err := ffmpeg.Command("-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	if err != nil {
		return "", err
	}
	out, _ := cmd.CombinedOutput() // список устройств ffmpeg печатает и сразу выходит с ошибкой — это нормально
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if m := dshowAudioRe.FindStringSubmatch(sc.Text()); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("микрофон не найден: подключи его и проверь в настройках звука Windows")
}
