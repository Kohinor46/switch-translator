package speech

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"switch-translator/internal/audio"
)

// Настоящие sherpa-onnx и голос Piper: SWTR_PIPER=1 go test ./internal/speech -run PiperLive -v
// (скачивает около 90 МБ во временную папку).
func TestPiperLive(t *testing.T) {
	if os.Getenv("SWTR_PIPER") == "" {
		t.Skip("SWTR_PIPER не задан")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), ".cache"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg := Config{Model: "irina"}
	if ok, _ := Ready("piper", cfg); ok {
		t.Fatal("готов до скачивания")
	}
	if err := Setup(ctx, "piper", cfg, func(s string) { t.Log(s) }, nil); err != nil {
		t.Fatal(err)
	}
	if ok, text := Ready("piper", cfg); !ok {
		t.Fatal(text)
	}
	tts, _ := NewSynthesizer("piper", cfg)
	start := time.Now()
	out, err := tts.Synthesize(ctx, "Делайте широкие размашистые движения.", filepath.Join(t.TempDir(), "p"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	p, err := audio.ReadWAV(b)
	if err != nil || p.Seconds() < 1 {
		t.Fatalf("%v", err)
	}
	t.Logf("%d Гц, %.1f с, за %v", p.Rate, p.Seconds(), time.Since(start).Round(time.Millisecond))
}
