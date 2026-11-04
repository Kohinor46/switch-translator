package oodle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Сжатые блоки из настоящей игры: SWTR_OODLE_DIR — папка с файлами <имя>_<блок>_<исходный размер>.bin.
func TestKrakenDir(t *testing.T) {
	dir := os.Getenv("SWTR_OODLE_DIR")
	if dir == "" {
		t.Skip("SWTR_OODLE_DIR не задан")
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*.bin"))
	bad := 0
	for _, n := range names {
		parts := strings.Split(strings.TrimSuffix(filepath.Base(n), ".bin"), "_")
		size, _ := strconv.Atoi(parts[len(parts)-1])
		b, _ := os.ReadFile(n)
		out, err := Decompress(b, size)
		if err != nil {
			bad++
			t.Errorf("%s: %v", filepath.Base(n), err)
			continue
		}
		if os.Getenv("SWTR_OODLE_SAVE") != "" {
			os.WriteFile(n+".out", out, 0o644)
		}
	}
	t.Logf("%d файлов, ошибок %d", len(names), bad)
}
