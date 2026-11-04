package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"testing"
)

func TestMsgPatterns(t *testing.T) {
	Set("en")
	defer Set("ru")
	m := strs
	// готовое сообщение с подставленными значениями находит свой шаблон
	for key, want := range m {
		if len(verbRe.FindAllString(key, -1)) != 1 || verbRe.FindString(key) != "%d" {
			continue
		}
		got := Msg(fmt.Sprintf(key, 42))
		if got != fmt.Sprintf(want, 42) {
			t.Errorf("%q → %q, ждали %q", key, got, fmt.Sprintf(want, 42))
		}
	}
	// обёрнутая ошибка переводится по частям
	inner := errors.New("ни один файл игры не изменился")
	if _, ok := m[inner.Error()]; ok {
		e := fmt.Errorf("Unity: %w", inner)
		if got := Err(e); got != "Unity: "+m[inner.Error()] {
			t.Errorf("%q", got)
		}
	}
}

func TestLocales(t *testing.T) {
	ents, _ := files.ReadDir("locales")
	var en map[string]string
	b, _ := files.ReadFile("locales/en.json")
	json.Unmarshal(b, &en)
	for _, e := range ents {
		var m map[string]string
		b, _ := files.ReadFile(path.Join("locales", e.Name()))
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for k, v := range m {
			if fmt.Sprint(verbRe.FindAllString(k, -1)) != fmt.Sprint(verbRe.FindAllString(v, -1)) {
				t.Errorf("%s: глаголы формата в %q", e.Name(), k)
			}
		}
		if len(m) < len(en)*9/10 {
			t.Errorf("%s: %d строк, в английском %d", e.Name(), len(m), len(en))
		}
	}
	if Match("pt_BR") != "pt" || Match("zh-Hans-CN") != "zh" || Match("ru_RU.UTF-8") != "ru" || Match("ko") != "en" {
		t.Error("Match")
	}
}
