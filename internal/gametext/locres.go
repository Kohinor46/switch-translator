package gametext

import (
	"bytes"
	"path"
	"strings"

	"switch-translator/internal/unity"
	"switch-translator/internal/unreal"
)

// Тексты игр на Unreal Engine: таблицы .locres (Content/Localization/<игра>/<язык>/<игра>.locres).
// Язык строки — папка над файлом: en, en-US, zh-Hans…

// IsLocres — похоже ли на .locres с подписью.
func IsLocres(b []byte) bool { return bytes.HasPrefix(b, []byte{0x0E, 0x14, 0x74, 0x75}) }

// LocresLocale — язык по пути к .locres.
func LocresLocale(p string) string { return unity.NormLang(path.Base(path.Dir(p))) }

// FromLocres — строки .locres. file — файл игры (сам .locres или архив .pak), inner — путь внутри архива.
// native — .locres исходного языка (или nil): ключи, которых в этом языке нет, игра показывает
// исходным текстом; они тоже попадают в список, чтобы перевести всю игру, а не только переопределённое.
func FromLocres(file, inner string, data, native []byte) ([]String, error) {
	l, err := unreal.ParseLocres(data)
	if err != nil {
		return nil, err
	}
	texts := l.Texts
	if native != nil {
		texts = append(texts, missingTexts(l, native)...)
	}
	where := path.Join(file, inner)
	loc := LocresLocale(where)
	var out []String
	for i, t := range texts {
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		label := t.Key
		if t.Namespace != "" {
			label = t.Namespace + "/" + t.Key
		}
		out = append(out, String{
			ID:     where + "#" + t.Namespace + "/" + t.Key,
			File:   file,
			Inner:  inner,
			Label:  label,
			Index:  i,
			Locale: loc,
			Source: t.Text,
			Skip:   !HasLetters(t.Text),
		})
	}
	return out, nil
}

// missingTexts — записи исходного .locres, которых нет в l (с исходным хэшем: по нему движок
// принимает перевод).
func missingTexts(l *unreal.Locres, native []byte) []unreal.LocText {
	n, err := unreal.ParseLocres(native)
	if err != nil {
		return nil
	}
	has := make(map[string]bool, len(l.Texts))
	for _, t := range l.Texts {
		has[t.Namespace+"/"+t.Key] = true
	}
	var out []unreal.LocText
	for _, t := range n.Texts {
		if !has[t.Namespace+"/"+t.Key] {
			out = append(out, t)
		}
	}
	return out
}

// ApplyLocres вписывает переводы в .locres (where — файл игры с путём внутри архива, как в String.ID).
// Переводы ключей, которых в файле нет, дописываются из исходного native (если он дан).
func ApplyLocres(where string, data, native []byte, tr map[string]string) ([]byte, int, error) {
	l, err := unreal.ParseLocres(data)
	if err != nil {
		return nil, 0, err
	}
	orig := len(l.Texts)
	if native != nil {
		l.Texts = append(l.Texts, missingTexts(l, native)...)
	}
	n := 0
	var keep []unreal.LocText
	for i, t := range l.Texts {
		v, ok := tr[where+"#"+t.Namespace+"/"+t.Key]
		if ok && v != t.Text {
			t.Text = v
			n++
		} else if i >= orig {
			continue // добавленный ключ без перевода не нужен: игра и так покажет исходный текст
		}
		keep = append(keep, t)
	}
	if n == 0 {
		return nil, 0, nil
	}
	l.Texts = keep
	return l.Bytes(), n, nil
}
