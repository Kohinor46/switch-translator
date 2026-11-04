package unity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// SWTR_BUNDLE=<папка с *.bundle>: узлы читаются, пересобранный без сжатия бандл даёт те же узлы.
func TestBundleReal(t *testing.T) {
	dir := os.Getenv("SWTR_BUNDLE")
	if dir == "" {
		t.Skip("SWTR_BUNDLE не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.bundle"))
	for _, p := range files {
		b, _ := os.ReadFile(p)
		bn, err := OpenBundle(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		nodes := map[string][]byte{}
		for _, n := range bn.Nodes {
			d, err := bn.ReadNode(bytes.NewReader(b), n)
			if err != nil {
				t.Fatalf("%s/%s: %v", filepath.Base(p), n.Path, err)
			}
			nodes[n.Path] = d
			t.Logf("%s: %s %d байт, начало %q", filepath.Base(p), n.Path, len(d), d[:min(8, len(d))])
		}
		out, err := bn.Rebuild(bytes.NewReader(b), nil)
		if err != nil {
			t.Fatal(err)
		}
		again, err := OpenBundle(bytes.NewReader(out), int64(len(out)))
		if err != nil {
			t.Fatalf("пересобранный не читается: %v", err)
		}
		for _, n := range again.Nodes {
			d, err := again.ReadNode(bytes.NewReader(out), n)
			if err != nil || !bytes.Equal(d, nodes[n.Path]) {
				t.Fatalf("%s: узел %s изменился: %v", filepath.Base(p), n.Path, err)
			}
		}
	}
}
