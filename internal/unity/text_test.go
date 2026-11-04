package unity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocCSV(t *testing.T) {
	src := "\xEF\xBB\xBF,en_US,fr,ru\r\nhello,Hello,\"Bon, jour\",\r\nq,\"Say \"\"hi\"\"\",Salut,Привет\r\n"
	rows, langs := parseLocCSV([]byte(src))
	if rows == nil || langs[1] != "en-US" || langs[2] != "fr" || langs[3] != "ru" {
		t.Fatalf("%v %v", rows, langs)
	}
	if rows[1][2].text != "Bon, jour" || rows[2][1].text != `Say "hi"` || rows[2][3].text != "Привет" {
		t.Fatalf("%+v", rows)
	}
	out := replaceCells([]byte(src), []cell{rows[1][1], rows[2][1]}, map[int]string{rows[1][1].start: "Привет, мир", rows[2][1].start: "Скажи"})
	want := "\xEF\xBB\xBF,en_US,fr,ru\r\nhello,\"Привет, мир\",\"Bon, jour\",\r\nq,\"Скажи\",Salut,Привет\r\n"
	if string(out) != want {
		t.Fatalf("%q", out)
	}
	if NormLang("es_mx") != "es-MX" || NormLang("zh-Hans") != "zh-Hans" || NormLang("Emotion") != "" || NormLang("es-419") != "es-419" {
		t.Fatal(NormLang("es_mx"), NormLang("zh-Hans"))
	}
}

// SWTR_UNITY=<папка Data> — у всех файлов с таблицами: английские строки переписываются, файл
// разбирается снова, остальные языки и прочие объекты не меняются.
func TestTextRoundTrip(t *testing.T) {
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
			t.Errorf("%s: %v", filepath.Base(p), err)
			return nil
		}
		es := Entries(f)
		if len(es) == 0 {
			return nil
		}
		langs := map[string]int{}
		tr := map[string]string{}
		for _, e := range es {
			langs[e.Lang]++
			if strings.HasPrefix(e.Lang, "en") {
				tr[e.ID()] = "Перевод, \"" + e.Text + "\""
			}
		}
		t.Logf("%s: строк %d, языки %v", filepath.Base(p), len(es), langs)
		out, n, err := Apply(f, tr)
		if err != nil || n != len(tr) {
			t.Fatalf("%s: вписано %d из %d: %v", filepath.Base(p), n, len(tr), err)
		}
		g, err := ParseSerialized(out)
		if err != nil {
			t.Fatalf("собранный файл не читается: %v", err)
		}
		if len(g.Objects) != len(f.Objects) {
			t.Fatalf("объектов %d, было %d", len(g.Objects), len(f.Objects))
		}
		got := Entries(g)
		if len(got) != len(es) {
			t.Fatalf("строк после сборки %d, было %d", len(got), len(es))
		}
		for i, e := range got {
			want := es[i].Text
			if w, ok := tr[e.ID()]; ok {
				want = w
			}
			if e.ID() != es[i].ID() || e.Text != want {
				t.Fatalf("%s: %q, ждали %s = %q", e.ID(), e.Text, es[i].ID(), want)
			}
		}
		for i, o := range f.Objects {
			if o.ClassID != ClassMonoBehaviour && o.ClassID != ClassTextAsset && string(f.Data(o)) != string(g.Data(g.Objects[i])) {
				t.Fatalf("объект %d изменился", o.PathID)
			}
		}
		return nil
	})
}
