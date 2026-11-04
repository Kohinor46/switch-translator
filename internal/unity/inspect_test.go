package unity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// SWTR_UNITY=<папка Data> — что лежит в ассетах: классы, TextAsset, скрипты MonoBehaviour.
func TestInspect(t *testing.T) {
	dir := os.Getenv("SWTR_UNITY")
	if dir == "" {
		t.Skip("SWTR_UNITY не задан")
	}
	files := map[string]*File{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if !IsSerialized(b) {
			return nil
		}
		f, err := ParseSerialized(b)
		if err != nil {
			fmt.Printf("%s: %v\n", filepath.Base(p), err)
			return nil
		}
		files[filepath.Base(p)] = f
		return nil
	})
	scripts := map[string]map[int64]string{}
	for name, f := range files {
		for _, o := range f.Objects {
			if o.ClassID == ClassMonoScript {
				if m, ok := f.Script(o); ok {
					if scripts[name] == nil {
						scripts[name] = map[int64]string{}
					}
					scripts[name][o.PathID] = m.Namespace + "." + m.Class
				}
			}
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	monos := map[string]int{}
	monoBytes := map[string]int{}
	for _, name := range names {
		f := files[name]
		cls := map[int32]int{}
		var ta []string
		for _, o := range f.Objects {
			cls[o.ClassID]++
			switch o.ClassID {
			case ClassTextAsset:
				ta = append(ta, fmt.Sprintf("%s(%d)", f.ObjectName(o), o.Size))
			case ClassMonoBehaviour:
				h, ok := f.Mono(o)
				if !ok {
					continue
				}
				ext := name
				if h.Script.File > 0 && int(h.Script.File) <= len(f.Externals) {
					ext = filepath.Base(f.Externals[h.Script.File-1].Path)
				}
				c := scripts[ext][h.Script.Path]
				if c == "" {
					c = fmt.Sprintf("?%s#%d", ext, h.Script.Path)
				}
				monos[c]++
				monoBytes[c] += int(o.Size)
			}
		}
		fmt.Printf("%s: v%d %s tree=%v объектов %d TextAsset %d MonoBehaviour %d\n", name, f.Version, f.Unity, f.TypeTree, len(f.Objects), cls[ClassTextAsset], cls[ClassMonoBehaviour])
		if len(ta) > 0 {
			if len(ta) > 30 {
				ta = ta[:30]
			}
			fmt.Printf("   TextAsset: %s\n", strings.Join(ta, " "))
		}
	}
	type kv struct {
		k    string
		n, b int
	}
	var list []kv
	for k, n := range monos {
		list = append(list, kv{k, n, monoBytes[k]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].b > list[j].b })
	for i, e := range list {
		if i == 40 {
			break
		}
		fmt.Printf("  MB %-60s ×%d %d КБ\n", e.k, e.n, e.b>>10)
	}
}

// SWTR_UNITY=<папка> — таблицы I2 в ассетах: языки и примеры терминов.
func TestInspectI2(t *testing.T) {
	dir := os.Getenv("SWTR_UNITY")
	if dir == "" {
		t.Skip("SWTR_UNITY не задан")
	}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if !IsSerialized(b) {
			return nil
		}
		f, err := ParseSerialized(b)
		if err != nil {
			return nil
		}
		for _, o := range f.Objects {
			if o.ClassID != ClassMonoBehaviour || o.Size < 2000 {
				continue
			}
			h, ok := f.Mono(o)
			if !ok {
				continue
			}
			d := f.Data(o)
			src, ok := ParseI2(d, h.Fields, f.Order())
			if !ok {
				continue
			}
			fmt.Printf("%s #%d: терминов %d, языков %d: %v\n", filepath.Base(p), o.PathID, len(src.Terms), len(src.Languages), src.Languages)
			for i, tm := range src.Terms {
				if i%max(1, len(src.Terms)/5) == 0 && len(tm.Values) > 0 {
					fmt.Printf("   %s = %q | %q\n", tm.Term, tm.Values[0].Text(d), tm.Values[len(tm.Values)-1].Text(d))
				}
			}
		}
		return nil
	})
}

// SWTR_UNITY_FILE=<файл> SWTR_UNITY_OUT=<папка> — выгрузить все TextAsset.
func TestExportText(t *testing.T) {
	p, out := os.Getenv("SWTR_UNITY_FILE"), os.Getenv("SWTR_UNITY_OUT")
	if p == "" || out == "" {
		t.Skip()
	}
	b, _ := os.ReadFile(p)
	f, err := ParseSerialized(b)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(out, 0o755)
	for _, o := range f.Objects {
		if o.ClassID == ClassTextAsset {
			name, sp, ok := f.TextScript(o)
			if ok {
				d := f.Data(o)
				os.WriteFile(filepath.Join(out, fmt.Sprintf("%d_%s", o.PathID, filepath.Base(name))), d[sp.Off+4:sp.Off+4+sp.Len], 0o644)
			}
		}
	}
}
