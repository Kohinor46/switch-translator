package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"switch-translator/internal/audio"
	"sync"
	"time"
)

// Голос в списке показывается как «Имя — id»; VoiceID достаёт id обратно.
func VoiceLabel(name, id string) string {
	if name == "" || name == id {
		return id
	}
	return name + " — " + id
}

func VoiceID(label string) string {
	if i := strings.LastIndex(label, " — "); i >= 0 {
		return strings.TrimSpace(label[i+len(" — "):])
	}
	return strings.TrimSpace(label)
}

var sayLine = regexp.MustCompile(`^(.+?)\s+([a-z]{2,3}_[A-Z0-9]{2,3})\s+#`)

// SayVoices — установленные голоса macOS для языка (ru, en…).
func SayVoices(lang string) []string {
	out, err := exec.Command("say", "-v", "?").Output()
	if err != nil {
		return nil
	}
	var voices []string
	for _, line := range strings.Split(string(out), "\n") {
		m := sayLine.FindStringSubmatch(line)
		if m != nil && strings.HasPrefix(m[2], lang+"_") {
			voices = append(voices, strings.TrimSpace(m[1]))
		}
	}
	return voices
}

// OpenAIVoices — голоса OpenAI TTS.
func OpenAIVoices() []string {
	return []string{"alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse"}
}

type Voice struct {
	ID, Name, PreviewURL string
}

// ElevenLabsVoices — голоса аккаунта ElevenLabs, включая скопированные.
func ElevenLabsVoices(ctx context.Context, key string) ([]Voice, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.elevenlabs.io/v1/voices", nil)
	req.Header.Set("xi-api-key", key)
	data, err := do(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Voices []struct {
			VoiceID    string `json:"voice_id"`
			Name       string `json:"name"`
			PreviewURL string `json:"preview_url"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]Voice, len(resp.Voices))
	for i, v := range resp.Voices {
		out[i] = Voice{v.VoiceID, v.Name, v.PreviewURL}
	}
	return out, nil
}

// cloneElevenLabs создаёт в ElevenLabs голос по образцам речи (Instant Voice Cloning) и возвращает его id.
func cloneElevenLabs(ctx context.Context, key, name string, samples []string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("name", name)
	mw.WriteField("remove_background_noise", "true")
	for _, p := range samples {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		fw, _ := mw.CreateFormFile("files", filepath.Base(p))
		_, err = io.Copy(fw, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	mw.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.elevenlabs.io/v1/voices/add", &body)
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	data, err := do(req)
	if err != nil {
		return "", err
	}
	var resp struct {
		VoiceID string `json:"voice_id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.VoiceID == "" {
		return "", fmt.Errorf("ElevenLabs не вернул голос: %.200s", data)
	}
	return resp.VoiceID, nil
}

// Download сохраняет файл по ссылке (например, образец голоса ElevenLabs).
func Download(ctx context.Context, url, dst string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	data, err := do(req)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// Проигрывание: один звук за раз, новый останавливает предыдущий.
var (
	playMu     sync.Mutex
	playing    *exec.Cmd
	playPaused bool

	// где сейчас воспроизведение: файл, с какой секунды запущен плеер, когда, сколько стоял на паузе
	playPath     string
	playFrom     float64
	playDur      float64
	playStarted  time.Time
	playPausedAt time.Time
	playPausedIn time.Duration
	playDone     func()
)

// Play проигрывает файл системным плеером и возвращается сразу; onDone вызывается по окончании.
func Play(path string, onDone func()) error { return PlayFrom(path, 0, onDone) }

// PlayFrom играет файл с секунды from. Системные плееры (afplay, paplay) перематывать не умеют,
// поэтому для WAV с середины запускается обрезанная копия. onDone вызывается, когда звук доиграл сам.
func PlayFrom(path string, from float64, onDone func()) error {
	StopPlayback()
	dur, play := 0.0, path
	if b, err := os.ReadFile(path); err == nil {
		if p, err := audio.ReadWAV(b); err == nil {
			dur = p.Seconds()
			from = max(0, min(from, dur))
			if from > 0.05 {
				start := int(from * float64(p.Rate))
				for c := range p.Channels {
					p.Channels[c] = p.Channels[c][min(start, len(p.Channels[c])):]
				}
				play = filepath.Join(os.TempDir(), "swtr-play.wav")
				if err := os.WriteFile(play, audio.WAV(p), 0o644); err != nil {
					return err
				}
			}
		} else {
			from = 0
		}
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("afplay", play)
	case "windows":
		ps := fmt.Sprintf(`Add-Type -AssemblyName presentationCore; $p = New-Object System.Windows.Media.MediaPlayer; $p.Open('%s'); `+
			`while (-not $p.NaturalDuration.HasTimeSpan) { Start-Sleep -Milliseconds 50 }; $p.Play(); `+
			`Start-Sleep -Milliseconds ([int]$p.NaturalDuration.TimeSpan.TotalMilliseconds + 200)`, strings.ReplaceAll(play, "'", "''"))
		cmd = exec.Command("powershell", "-NoProfile", "-Command", ps)
	default:
		bin, err := findTool("paplay", "aplay", "ffplay")
		if err != nil {
			return fmt.Errorf("не найден плеер звука: установи pulseaudio-utils или ffmpeg")
		}
		cmd = exec.Command(bin, play)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	playMu.Lock()
	playing, playPaused = cmd, false
	playPath, playFrom, playDur, playStarted, playPausedIn, playDone = path, from, dur, time.Now(), 0, onDone
	playMu.Unlock()
	go func() {
		cmd.Wait()
		playMu.Lock()
		natural := playing == cmd // не остановлен и не перезапущен с другого места
		if natural {
			playing, playPath = nil, ""
		}
		playMu.Unlock()
		if natural && onDone != nil {
			onDone()
		}
	}()
	return nil
}

// PlayState — что играет: файл, позиция и длина в секундах (длина 0 — неизвестна, перемотки нет).
type PlayState struct {
	Path     string
	Pos, Dur float64
	Paused   bool
}

// State — текущее воспроизведение; пустой Path — ничего не играет.
func State() PlayState {
	playMu.Lock()
	defer playMu.Unlock()
	if playing == nil {
		return PlayState{}
	}
	now := time.Now()
	if playPaused {
		now = playPausedAt
	}
	pos := playFrom + (now.Sub(playStarted) - playPausedIn).Seconds()
	if playDur > 0 {
		pos = min(pos, playDur)
	}
	return PlayState{Path: playPath, Pos: pos, Dur: playDur, Paused: playPaused}
}

// Seek перематывает текущий файл; если стоял на паузе — остаётся на паузе.
func Seek(sec float64) error {
	playMu.Lock()
	path, paused, done := playPath, playPaused, playDone
	playMu.Unlock()
	if path == "" {
		return nil
	}
	if err := PlayFrom(path, sec, done); err != nil {
		return err
	}
	if paused {
		PausePlayback()
	}
	return nil
}

func StopPlayback() {
	playMu.Lock()
	defer playMu.Unlock()
	if playing != nil && playing.Process != nil {
		if playPaused {
			playing.Process.Signal(os.Interrupt) // замороженный процесс тоже должен умереть
		}
		playing.Process.Kill()
		playing, playPaused, playPath = nil, false, ""
	}
}

// Sample — фраза для прослушивания голоса.
func Sample(lang string) string {
	switch lang {
	case "ru":
		return "Привет! Так будет звучать перевод этой игры."
	case "uk":
		return "Привіт! Так звучатиме переклад цієї гри."
	}
	return "Hello! This is how the translation of this game will sound."
}
