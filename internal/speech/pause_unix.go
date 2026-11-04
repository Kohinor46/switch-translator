//go:build !windows

package speech

import (
	"syscall"
	"time"
)

// PausePlayback ставит прослушивание на паузу или продолжает его. Возвращает true — сейчас на паузе.
// Плеер (afplay, paplay) просто замораживается сигналом и потом размораживается.
func PausePlayback() (paused bool, ok bool) {
	playMu.Lock()
	defer playMu.Unlock()
	if playing == nil || playing.Process == nil {
		return false, false
	}
	sig := syscall.SIGSTOP
	if playPaused {
		sig = syscall.SIGCONT
	}
	if err := playing.Process.Signal(sig); err != nil {
		return playPaused, false
	}
	if playPaused {
		playPausedIn += time.Since(playPausedAt)
	} else {
		playPausedAt = time.Now()
	}
	playPaused = !playPaused
	return playPaused, true
}

// CanPause — умеет ли эта система ставить звук на паузу.
func CanPause() bool { return true }
