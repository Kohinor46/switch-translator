package gui

import (
	"runtime"
	"syscall"
)

var setThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// keepAwake не даёт компьютеру уснуть, пока идёт долгая задача (озвучка может идти часами);
// release возвращает всё как было. Запрет Windows привязан к потоку, поэтому его ставит и снимает
// один и тот же поток; экран при этом гаснуть может.
func keepAwake(string) (release func()) {
	if setThreadExecutionState.Find() != nil {
		return func() {}
	}
	done := make(chan struct{})
	set := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		setThreadExecutionState.Call(esContinuous | esSystemRequired)
		close(set)
		<-done
		setThreadExecutionState.Call(esContinuous)
	}()
	<-set
	return func() { close(done) }
}
