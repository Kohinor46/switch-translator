package gui

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Пока задача идёт — в системе на один запрет сна от caffeinate больше, после release — как было.
func TestKeepAwake(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("проверка через pmset — только на macOS")
	}
	held := func() int {
		out, err := exec.Command("pmset", "-g", "assertions").Output()
		if err != nil {
			t.Skip("pmset недоступен")
		}
		return strings.Count(string(out), `PreventUserIdleSystemSleep named: "caffeinate command-line tool"`)
	}
	// caffeinate ставит запрет не мгновенно
	wait := func(want int) bool {
		for range 40 {
			if held() == want {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}
	before := held()
	release := keepAwake("проверка")
	if !wait(before + 1) {
		t.Fatal("запрет сна не поставлен")
	}
	release()
	if !wait(before) {
		t.Fatal("запрет сна не снят")
	}
}
