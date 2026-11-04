package whisper

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SWTR_WHISPER_MODEL=~/.cache/whisper/ggml-base.bin go test ./internal/whisper -v
func TestTranscribe(t *testing.T) {
	model := os.Getenv("SWTR_WHISPER_MODEL")
	if model == "" {
		t.Skip("SWTR_WHISPER_MODEL не задан")
	}
	if _, err := exec.LookPath("say"); err != nil {
		t.Skip("нет say")
	}
	raw := filepath.Join(t.TempDir(), "speech.wav")
	if out, err := exec.Command("say", "-v", "Samantha", "-o", raw, "--data-format=LEF32@16000", "Press the button to jump over the river").CombinedOutput(); err != nil {
		t.Skip(string(out))
	}
	data, _ := os.ReadFile(raw)
	// WAV: данные после чанка «data» (4 байта имени, 4 — размера)
	if i := strings.Index(string(data), "data"); i >= 0 {
		data = data[i+8:]
	}
	samples := make([]float32, len(data)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	m, err := Load(model)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	start := time.Now()
	text, err := m.Transcribe(samples, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%.1f с звука за %v: %q", float64(len(samples))/SampleRate, time.Since(start), text)
	if !strings.Contains(strings.ToLower(text), "jump") {
		t.Fatalf("не распознано: %q", text)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"[MUZI]":                              "",
		"1/2 oz 1/2 oz [MUZI]":                "",
		"♪ (music) ♪":                         "",
		"Oh no!":                              "Oh no!",
		" Press  the button [beep]":           "Press the button",
		"AH" + strings.Repeat("H", 700) + "!": "AHHHHH!",
		"Good! Keep it up!":                   "Good! Keep it up!",
		"Whoooa, that was close.":             "Whoooa, that was close.",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestRepetitive(t *testing.T) {
	for text, want := range map[string]string{
		"Thank you. Thank you. Thank you.":                                      "",
		"And drop down, say. And drop down, say. And drop down, say. It's time": "",
		"Thank you! See you tomorrow, hero.":                                    "Thank you! See you tomorrow, hero.",
		"Yes! Yes! Let's go, everyone, let's go now!":                           "Yes! Yes! Let's go, everyone, let's go now!",
	} {
		if got := Clean(text); got != want {
			t.Errorf("%q → %q", text, got)
		}
	}
}

func TestCleanShort(t *testing.T) {
	for text, want := range map[string]string{
		"Go!":      "Go!",
		"Perfect!": "Perfect!",
		"You":      "",
		"Exhale as you relax your oblique muscles. Exhale as you relax your oblique muscles.": "Exhale as you relax your oblique muscles.",
	} {
		if got := Clean(text); got != want {
			t.Errorf("%q → %q", text, got)
		}
	}
}
