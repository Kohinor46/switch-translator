package speech

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SWTR_STT_FILES=a.bfstm:b.bfstm SWTR_WHISPER_MODEL=base go test ./internal/speech -run LocalSTT -v
func TestLocalSTT(t *testing.T) {
	files := filepath.SplitList(os.Getenv("SWTR_STT_FILES"))
	if len(files) == 0 {
		t.Skip("SWTR_STT_FILES не задан")
	}
	rec, err := NewRecognizer("whispercpp", Config{Model: os.Getenv("SWTR_WHISPER_MODEL")})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		wav := filepath.Join(t.TempDir(), "x.wav")
		if err := DecodeToWav(context.Background(), f, wav); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		text, err := rec.Transcribe(context.Background(), wav)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s за %v: %q", filepath.Base(f), time.Since(start).Round(time.Millisecond), text)
	}
}
