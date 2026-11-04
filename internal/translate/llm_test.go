package translate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Модель видит короткие {{1}}, {{2}}, а в перевод возвращаются исходные коды игры.
// Строку, где модель потеряла код, переспрашиваем; строки без букв не отправляем вовсе.
func TestLLMShortTags(t *testing.T) {
	long := "{{10:1:1800530075007200700072006900730065005f004c004c003100}}"
	var asked [][]string
	calls := 0
	l := &llm{call: func(_ context.Context, _ Config, _, user string) (string, error) {
		calls++
		var in []string
		json.Unmarshal([]byte(user[strings.Index(user, "["):]), &in)
		asked = append(asked, in)
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = strings.ReplaceAll(strings.ReplaceAll(s, "Hello", "Привет"), "Bye", "Пока")
			if calls == 1 && strings.Contains(s, "Bye") {
				out[i] = "Пока" // потерял код
			}
		}
		b, _ := json.Marshal(map[string][]string{"translations": out})
		return string(b), nil
	}}
	got, err := l.Translate(context.Background(), []string{long + "Hello {{3:0:}}!", long, "{{1:2:ab}}Bye"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{long + "Привет {{3:0:}}!", long, "{{1:2:ab}}Пока"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q", i, got[i])
		}
	}
	if asked[0][0] != "{{1}}Hello {{2}}!" || len(asked[0]) != 2 || len(asked) != 2 || len(asked[1]) != 1 {
		t.Errorf("отправлено %q", asked)
	}
}

// С пачкой уходят только статьи словаря, которые в ней встречаются, — целыми словами.
func TestGlossaryFor(t *testing.T) {
	g := []Term{{Source: "Toad", Translation: "Тоад"}, {Source: "Ring", Translation: "Ринг"}, {Source: "My Menu", Translation: "Моё меню"},
		{Source: "トード", Translation: "Тоад"}, {Source: "Empty"}}
	got := glossaryFor(g, []string{"Toadette and Toad's ring", "Open my menu", "トードです", "Empty here"})
	var names []string
	for _, t := range got {
		names = append(names, t.Source)
	}
	if strings.Join(names, ",") != "Toad,Ring,My Menu,トード" {
		t.Errorf("%v", names)
	}
	if got := glossaryFor(g, []string{"Toadette"}); len(got) != 0 {
		t.Errorf("Toad внутри Toadette: %v", got)
	}
}
