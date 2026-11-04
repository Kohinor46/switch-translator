package project

import (
	"os"
	"path/filepath"
	"testing"

	"switch-translator/internal/gametext"
)

// TestCompareWithMod сравнивает собранный файл с файлом готового мода: те же реплики, те же метки.
// SWTR_OURS=<наш .szs> SWTR_THEIRS=<.szs из мода> go test ./internal/project -run CompareWithMod -v
func TestCompareWithMod(t *testing.T) {
	ours, theirs := os.Getenv("SWTR_OURS"), os.Getenv("SWTR_THEIRS")
	if ours == "" || theirs == "" {
		t.Skip("SWTR_OURS/SWTR_THEIRS не заданы")
	}
	read := func(p string) map[string]string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		strs, err := gametext.Extract(filepath.Base(p), b)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, s := range strs {
			m[s.ID] = s.Source
		}
		return m
	}
	a, b := read(ours), read(theirs)
	missing := 0
	for id := range b {
		if _, ok := a[id]; !ok {
			missing++
			t.Logf("нет у нас: %s = %q", id, b[id])
		}
	}
	t.Logf("у нас %d реплик, в моде %d, не хватает %d", len(a), len(b), missing)
	n := 0
	for id, v := range b {
		if n < 3 {
			t.Logf("%s\n  мод: %q\n  мы:  %q", id, v, a[id])
			n++
		}
	}
}
