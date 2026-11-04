package speech

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"switch-translator/internal/audio"
)

// SWTR_MIC=1 go test -run TestRecord — запись двух секунд с микрофона.
func TestRecord(t *testing.T) {
	if os.Getenv("SWTR_MIC") == "" {
		t.Skip("SWTR_MIC не задан")
	}
	out := filepath.Join(t.TempDir(), "mic.wav")
	r, err := StartRecording(out)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	p, err := audio.ReadWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("записано %.2f с, %d Гц, %d каналов", p.Seconds(), p.Rate, len(p.Channels))
	if p.Seconds() < 1.5 {
		t.Errorf("слишком коротко: %.2f", p.Seconds())
	}
}
