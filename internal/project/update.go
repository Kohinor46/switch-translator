package project

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	titleIDRe = regexp.MustCompile(`\[([0-9A-Fa-f]{16})\]`)
	versionRe = regexp.MustCompile(`\[v(\d+)\]`)
)

// FindExtras ищет рядом с образом игры обновления и DLC по title id в имени файла:
// у обновления — id игры + 0x800 («…4000» → «…4800»), у DLC — id игры + 0x1000 + номер («…5001»).
// Из обновлений берётся самое новое, из DLC — по самой новой версии каждого.
func FindExtras(gamePath string) []string {
	base, ok := nameTitleID(filepath.Base(gamePath))
	if !ok || base&0xFFF != 0 {
		return nil
	}
	type best struct {
		path string
		v    int
	}
	found := map[uint64]best{}
	dir := filepath.Dir(gamePath)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if e.IsDir() || (ext != ".nsp" && ext != ".nsz") {
			continue
		}
		tid, ok := nameTitleID(name)
		if !ok || AppOf(tid) != base || tid == base {
			continue
		}
		v := 0
		if vm := versionRe.FindStringSubmatch(name); vm != nil {
			v, _ = strconv.Atoi(vm[1])
		}
		key := tid
		if b, ok := found[key]; !ok || v > b.v {
			found[key] = best{filepath.Join(dir, name), v}
		}
	}
	var out []string
	for _, b := range found {
		out = append(out, b.path)
	}
	sort.Strings(out)
	return out
}

func nameTitleID(name string) (uint64, bool) {
	m := titleIDRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	tid, err := strconv.ParseUint(m[1], 16, 64)
	return tid, err == nil
}

// AppOf — title id игры, к которой относится title id игры, обновления или DLC.
func AppOf(tid uint64) uint64 {
	switch {
	case tid&0xFFF == 0:
		return tid
	case tid&0xFFF == 0x800:
		return tid &^ 0x800
	}
	return (tid - 0x1000) &^ 0xFFF
}

// IsExtraName — похоже ли имя файла на обновление или DLC (title id не кончается на 000).
func IsExtraName(name string) bool {
	tid, ok := nameTitleID(name)
	return ok && tid&0xFFF != 0
}

// IsUpdateName — похоже ли имя файла на обновление: title id оканчивается на 800.
func IsUpdateName(name string) bool {
	tid, ok := nameTitleID(name)
	return ok && tid&0xFFF == 0x800
}
