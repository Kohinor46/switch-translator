package speech

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Зависший say не должен вешать озвучку: его обрывают, пробуют ещё раз и пропускают фразу.
func TestSayHangs(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho x >> " + calls + "\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(bin, "say"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := sayTimeout
	sayTimeout = 200 * time.Millisecond
	defer func() { sayTimeout = old }()

	start := time.Now()
	_, err := (&macSay{}).Synthesize(context.Background(), "Да.", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "завис") {
		t.Fatalf("ошибка: %v", err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("ждали %v", el)
	}
	b, _ := os.ReadFile(calls)
	if n := strings.Count(string(b), "x"); n != 2 {
		t.Errorf("say запускался %d раз", n)
	}
}
