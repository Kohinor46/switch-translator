package gametext

import (
	"path"
	"strings"

	"switch-translator/internal/unity"
)

// Таблицы локализации CSV вне ассетов Unity: stuff/text/text.csv игр LEGO — колонка на язык
// (ENGLISH, RUSSIAN…), первая колонка — ключ строки.

// FromTable — строки таблицы. file — файл игры (архив .DAT), inner — путь внутри.
func FromTable(file, inner string, data []byte) []String {
	where := path.Join(file, inner)
	var out []String
	for i, t := range unity.CSVTexts(data) {
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		out = append(out, String{
			ID:     where + "#" + t.ID(),
			File:   file,
			Inner:  inner,
			Label:  t.Key,
			Index:  i,
			Locale: t.Lang,
			Source: t.Text,
			Skip:   !HasLetters(t.Text),
		})
	}
	return out
}

// ApplyTable вписывает переводы в таблицу (where — как в String.ID до «#»).
func ApplyTable(where string, data []byte, tr map[string]string) ([]byte, int) {
	own := map[string]string{}
	for id, t := range tr {
		if rest, ok := strings.CutPrefix(id, where+"#"); ok {
			own[rest] = t
		}
	}
	if len(own) == 0 {
		return nil, 0
	}
	return unity.ReplaceCSV(data, own)
}
