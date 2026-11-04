package unity

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// SWTR_UNITY_FILE=<файл> SWTR_UNITY_SCRIPT=<класс> — шестнадцатеричный дамп начала MonoBehaviour этого класса.
func TestDumpMono(t *testing.T) {
	p, cls := os.Getenv("SWTR_UNITY_FILE"), os.Getenv("SWTR_UNITY_SCRIPT")
	if p == "" {
		t.Skip()
	}
	n, _ := strconv.Atoi(os.Getenv("SWTR_UNITY_BYTES"))
	if n == 0 {
		n = 1024
	}
	b, _ := os.ReadFile(p)
	f, err := ParseSerialized(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range f.Objects {
		if o.ClassID != ClassMonoBehaviour || o.Size < 100000 {
			continue
		}
		h, _ := f.Mono(o)
		fmt.Printf("path %d size %d script %+v name %q fields at %d %s\n", o.PathID, o.Size, h.Script, h.Name, h.Fields, cls)
		d := f.Data(o)
		fmt.Print(hex.Dump(d[:min(n, len(d))]))
	}
}
