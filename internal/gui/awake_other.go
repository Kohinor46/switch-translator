//go:build !windows

package gui

import (
	"os/exec"
	"runtime"
)

// keepAwake не даёт компьютеру уснуть, пока идёт долгая задача (озвучка может идти часами);
// release возвращает всё как было. На macOS — caffeinate, на Linux — systemd-inhibit, если есть.
// Они держат запрет, пока живёт cat, а cat живёт, пока открыт его вход: закрыли — запрет снят,
// и даже если программа упадёт, вход закроется сам.
func keepAwake(what string) (release func()) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// -i: не засыпать от бездействия, -s: и от зарядки тоже; экран гаснуть может
		cmd = exec.Command("/usr/bin/caffeinate", "-i", "-s", "cat")
	default:
		path, err := exec.LookPath("systemd-inhibit")
		if err != nil {
			return func() {}
		}
		cmd = exec.Command(path, "--what=idle:sleep", "--who=Switch Translator", "--why="+what, "cat")
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return func() {}
	}
	if err := cmd.Start(); err != nil {
		return func() {}
	}
	return func() {
		in.Close()
		cmd.Wait()
	}
}
