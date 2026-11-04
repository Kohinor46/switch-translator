package speech

// PausePlayback: в Windows звук играет PowerShell, заморозить его нельзя.
func PausePlayback() (paused bool, ok bool) { return false, false }

// CanPause — умеет ли эта система ставить звук на паузу.
func CanPause() bool { return false }
