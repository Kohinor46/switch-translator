//go:build darwin

package speech

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"switch-translator/internal/audio"
)

// Плеер на беззвучном WAV: запуск с середины, пауза, перемотка на паузе, остановка.
func TestPlayerSeekPause(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "silence.wav")
	os.WriteFile(wav, audio.WAV(&audio.PCM{Rate: 16000, Channels: [][]int16{make([]int16, 16000*4)}}), 0o644)
	if err := PlayFrom(wav, 1, nil); err != nil {
		t.Fatal(err)
	}
	defer StopPlayback()
	time.Sleep(500 * time.Millisecond)
	st := State()
	if st.Path != wav || st.Dur != 4 || st.Pos < 1.3 || st.Pos > 1.8 {
		t.Fatalf("после запуска с 1 с: %+v", st)
	}
	if paused, ok := PausePlayback(); !ok || !paused {
		t.Fatal("пауза не сработала")
	}
	p1 := State().Pos
	time.Sleep(400 * time.Millisecond)
	if p2 := State().Pos; math.Abs(p2-p1) > 0.01 {
		t.Fatalf("на паузе позиция бежит: %.2f → %.2f", p1, p2)
	}
	if err := Seek(3); err != nil {
		t.Fatal(err)
	}
	if st := State(); !st.Paused || math.Abs(st.Pos-3) > 0.05 {
		t.Fatalf("после перемотки на паузе: %+v", st)
	}
	PausePlayback()
	time.Sleep(300 * time.Millisecond)
	if st := State(); st.Paused || st.Pos < 3.2 {
		t.Fatalf("после продолжения: %+v", st)
	}
	StopPlayback()
	if State().Path != "" {
		t.Fatal("не остановилось")
	}
}
