package gametext

import (
	"strings"

	"switch-translator/internal/unity"
)

// Тексты игр на Unity: таблицы I2 Localization и CSV-таблицы в TextAsset (см. пакет unity).
// Код языка строки — код колонки таблицы (en-US, fr, ru…): он и служит «версией текста».

func fromUnity(file string, data []byte) ([]String, error) {
	f, err := unity.ParseSerialized(data)
	if err != nil {
		return nil, err
	}
	var out []String
	for i, e := range unity.Entries(f) {
		if strings.TrimSpace(e.Text) == "" {
			continue
		}
		key := e.Key
		if _, term, ok := strings.Cut(key, "/"); ok {
			key = term
		}
		out = append(out, String{
			ID:     file + "#" + e.ID(),
			File:   file,
			Label:  key,
			Index:  i,
			Locale: e.Lang,
			Source: e.Text,
			Skip:   !HasLetters(e.Text),
		})
	}
	return out, nil
}

func applyUnity(file string, data []byte, tr map[string]string) ([]byte, int, error) {
	prefix := file + "#"
	own := map[string]string{}
	for id, t := range tr {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			own[rest] = t
		}
	}
	if len(own) == 0 {
		return nil, 0, nil
	}
	f, err := unity.ParseSerialized(data)
	if err != nil {
		return nil, 0, err
	}
	return unity.Apply(f, own)
}
