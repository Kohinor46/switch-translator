package translate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Живая проверка перевода через установленные Codex и Claude Code (тратит немного подписки).
// Запуск: SWTR_LIVE=codex,claudecode go test ./internal/translate -run Live -v
func TestLiveCLI(t *testing.T) {
	ids := os.Getenv("SWTR_LIVE")
	if ids == "" {
		t.Skip("SWTR_LIVE не задан")
	}
	for _, id := range []string{"codex", "claudecode"} {
		if !contains(ids, id) {
			continue
		}
		t.Run(id, func(t *testing.T) {
			tr, err := New(id, Config{From: "en", To: "ru"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			start := time.Now()
			got, err := tr.Translate(ctx, []string{"Reset camera", "Press {{0:3:0100}} to jump!"})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%v за %s", got, time.Since(start).Round(time.Second))
			if len(got) != 2 || !contains(got[1], "{{0:3:0100}}") {
				t.Errorf("ответ не тот: %q", got)
			}
		})
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
