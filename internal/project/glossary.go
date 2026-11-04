package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"switch-translator/internal/gametext"
	"switch-translator/internal/translate"
)

// Словарь игры. Весь текст — реплики и распознанная речь — режется на слова; из уникальных слов
// выбираются термины: имена, места, предметы, приёмы, пункты меню. Они переводятся один раз,
// их можно поправить руками, а потом переводчик переводит фразы целиком, сверяясь со словарём, —
// так «Toad» везде будет «Тоад», а не то «Жаба», то «Тоуд». Переводить фразы по словам нельзя:
// пропадут падежи и порядок слов.

// Term — статья словаря.
type Term struct {
	Source      string `json:"source"`
	Translation string `json:"translation,omitempty"`
	Count       int    `json:"count"`             // сколько раз встречается
	Example     string `json:"example,omitempty"` // фраза, где встречается
	Off         bool   `json:"off,omitempty"`     // не подсказывать переводчику
	Hand        bool   `json:"hand,omitempty"`    // добавлен или поправлен вручную
}

// WordStats — сколько слов во всём тексте и сколько из них разных.
type WordStats struct{ Words, Unique int }

const maxTerms = 3000

// CollectTerms режет тексты на слова и выбирает из них термины:
//   - слово с большой буквы посреди предложения («…ask Toad»), которое строчными почти не пишется
//     (так отсеиваются «You», «Great» после запятой или многоточия);
//   - сочетание слов с большой буквы («Power Moon», «Start Game», «Ms. Honey»);
//   - японское слово катаканой.
//
// Термин встречается хотя бы дважды: единожды встреченное слово согласовывать не с чем.
func CollectTerms(texts []string) ([]Term, WordStats) {
	type cand struct {
		count, mid int
		multi      bool
		example    string
	}
	cands := map[string]*cand{}
	add := func(term string, mid, multi bool, text string) {
		c := cands[term]
		if c == nil {
			c = &cand{example: text}
			cands[term] = c
		}
		c.count++
		if mid {
			c.mid++
		}
		c.multi = c.multi || multi
	}
	// первый проход — слова и сколько раз каждое написано строчными
	lower := map[string]int{}
	unique := map[string]bool{}
	var st WordStats
	type text struct {
		rs      []rune
		toks    []token
		example string
	}
	all := make([]text, 0, len(texts))
	for _, raw := range texts {
		t := strings.TrimSpace(gametext.StripCodes(raw))
		rs := []rune(t)
		toks := tokenize(rs)
		for _, tk := range toks {
			w := strings.ToLower(string(rs[tk.from:tk.to]))
			st.Words++
			unique[w] = true
			if !tk.kana && !unicode.IsUpper(rs[tk.from]) {
				lower[w]++
			}
		}
		all = append(all, text{rs, toks, shorten(strings.Join(strings.Fields(t), " "), 140)})
	}
	// второй проход — подряд идущие слова с большой буквы складываются в сочетание
	for _, tx := range all {
		rs, toks := tx.rs, tx.toks
		word := func(k int) string { return string(rs[toks[k].from:toks[k].to]) }
		for i := 0; i < len(toks); {
			t := toks[i]
			if t.kana {
				if t.to-t.from >= 2 {
					add(word(i), true, false, tx.example)
				}
				i++
				continue
			}
			if !capital(rs[t.from:t.to]) {
				i++
				continue
			}
			j := i + 1
			for j < len(toks) && toks[j].joined && capital(rs[toks[j].from:toks[j].to]) {
				j++
			}
			from, mid := i, !t.start
			// «Open My Menu» в начале предложения: первое слово — обычное (пишется и строчными), в термин не входит
			if t.start && j-i > 1 && lower[strings.ToLower(word(i))] > 0 {
				from, mid = i+1, true
			}
			add(possessive(string(rs[toks[from].from:toks[j-1].to])), mid, j-from > 1, tx.example)
			i = j
		}
	}
	st.Unique = len(unique)
	var out []Term
	for term, c := range cands {
		if c.count < 2 {
			continue
		}
		if c.multi || c.mid > 0 && lower[strings.ToLower(term)] < c.mid {
			out = append(out, Term{Source: term, Count: c.count, Example: c.example})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Source < out[j].Source
	})
	if len(out) > maxTerms {
		out = out[:maxTerms]
	}
	return out, st
}

type token struct {
	from, to int  // руны текста
	start    bool // в начале предложения: большая буква там ничего не значит
	joined   bool // от прошлого слова отделено одним пробелом (или «Ms. ») — может быть частью сочетания
	kana     bool // японское слово катаканой
}

// titles — сокращения, после точки в которых предложение не кончается.
var titles = map[string]bool{"Mr": true, "Mrs": true, "Ms": true, "Dr": true, "St": true, "Prof": true}

// tokenize режет текст на слова. Апостроф и дефис внутри слова — часть слова (Bowser's, Ring-Con).
func tokenize(rs []rune) []token {
	var toks []token
	start, gap := true, ""
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.In(r, unicode.Katakana) && r != '・':
			j := i
			for j < len(rs) && (unicode.In(rs[j], unicode.Katakana) || rs[j] == 'ー') && rs[j] != '・' {
				j++
			}
			toks = append(toks, token{from: i, to: j, kana: true})
			i, start, gap = j, false, ""
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			j := i + 1
			for j < len(rs) {
				if unicode.IsLetter(rs[j]) && !unicode.In(rs[j], unicode.Katakana) || unicode.IsDigit(rs[j]) {
					j++
					continue
				}
				if (rs[j] == '\'' || rs[j] == '’' || rs[j] == '-') && j+1 < len(rs) && unicode.IsLetter(rs[j+1]) {
					j += 2
					continue
				}
				break
			}
			// слова без пробелов (кандзи, хирагана) не разрезать — кусок целиком, термином не берём
			cjk := unicode.In(r, unicode.Han, unicode.Hiragana)
			toks = append(toks, token{from: i, to: j, start: start || cjk, joined: gap == " " && !cjk})
			i, start, gap = j, false, ""
			if i < len(rs) && rs[i] == '.' && titles[string(rs[toks[len(toks)-1].from:i])] {
				i++ // «Ms. Honey»: точка не конец предложения
			}
		default:
			if strings.ContainsRune(".!?:;\n\"«»“”()[]—…。！？「」『』", r) {
				start = true
			}
			if r == ' ' && gap == "" {
				gap = " "
			} else {
				gap = "x"
			}
			i++
		}
	}
	return toks
}

// capital — слово с большой буквы, кроме одиночных букв, «I'm», «I'll» и коротких сокращений вроде A, ZL, OK (кнопки).
func capital(rs []rune) bool {
	if len(rs) < 2 || !unicode.IsUpper(rs[0]) {
		return false
	}
	if rs[0] == 'I' && (rs[1] == '\'' || rs[1] == '’') {
		return false
	}
	if len(rs) <= 3 {
		for _, r := range rs {
			if unicode.IsLower(r) {
				return true
			}
		}
		return false
	}
	return true
}

// possessive убирает притяжательное «'s»: «Dragaux's» → «Dragaux».
func possessive(s string) string {
	for _, suf := range []string{"'s", "’s"} {
		if t, ok := strings.CutSuffix(s, suf); ok && t != "" {
			return t
		}
	}
	return s
}

func shorten(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}

// glossarySources — весь текст, который будет переводиться: отмеченные реплики, речь из звука и роликов.
func (s *Session) glossarySources() []string {
	var texts []string
	for _, str := range s.Strings {
		if !str.Skip && gametext.HasLetters(str.Source) {
			texts = append(texts, str.Source)
		}
	}
	for _, l := range s.Audio {
		if l.Transcript != "" {
			texts = append(texts, l.Transcript)
		}
	}
	for _, v := range s.Video {
		for _, sg := range v.Segments {
			if sg.Text != "" {
				texts = append(texts, sg.Text)
			}
		}
	}
	return texts
}

func (s *Session) glossaryPath(to string) string {
	name := "glossary." + to + ".json"
	if s.Locale != "" {
		name = "glossary." + s.Locale + "-" + to + ".json"
	}
	return filepath.Join(s.WorkDir, name)
}

// LoadGlossary подтягивает словарь для языка перевода.
func (s *Session) LoadGlossary(to string) {
	s.Glossary = nil
	if b, err := os.ReadFile(s.glossaryPath(to)); err == nil {
		json.Unmarshal(b, &s.Glossary)
	}
	s.CountWords()
}

// CountWords пересчитывает слова в тексте и речи (после распознавания их становится больше).
func (s *Session) CountWords() { _, s.Words = CollectTerms(s.glossarySources()) }

// SaveGlossary пишет словарь на диск.
func (s *Session) SaveGlossary(to string) error { return writeJSON(s.glossaryPath(to), s.Glossary) }

// BuildGlossary собирает словарь заново из текущего текста. Переводы, ручные статьи
// и отметки «не подсказывать» сохраняются.
func (s *Session) BuildGlossary(to string) error {
	terms, st := CollectTerms(s.glossarySources())
	s.Words = st
	prev := map[string]Term{}
	for _, t := range s.Glossary {
		prev[t.Source] = t
	}
	seen := map[string]bool{}
	for i, t := range terms {
		if p, ok := prev[t.Source]; ok {
			terms[i].Translation, terms[i].Off, terms[i].Hand = p.Translation, p.Off, p.Hand
		}
		seen[t.Source] = true
	}
	// добавленное руками остаётся, даже если автоматически не нашлось
	for _, t := range s.Glossary {
		if t.Hand && !seen[t.Source] {
			terms = append(terms, t)
		}
	}
	s.Glossary = terms
	s.log("Слов в тексте и речи: %d, разных: %d. В словаре %d терминов — имена, места, предметы, пункты меню",
		st.Words, st.Unique, len(terms))
	return s.SaveGlossary(to)
}

// AddTerm добавляет статью вручную (или правит существующую).
func (s *Session) AddTerm(to, source, translation string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("впиши слово")
	}
	for i := range s.Glossary {
		if s.Glossary[i].Source == source {
			s.Glossary[i].Translation, s.Glossary[i].Hand, s.Glossary[i].Off = strings.TrimSpace(translation), true, false
			return s.SaveGlossary(to)
		}
	}
	n := 0
	for _, t := range s.glossarySources() {
		if strings.Contains(strings.ToLower(t), strings.ToLower(source)) {
			n++
		}
	}
	s.Glossary = append([]Term{{Source: source, Translation: strings.TrimSpace(translation), Count: n, Hand: true}}, s.Glossary...)
	return s.SaveGlossary(to)
}

// TranslateGlossary переводит статьи словаря без перевода и сохраняет после каждой пачки.
func (s *Session) TranslateGlossary(ctx context.Context, tr translate.Translator, to string, progress Progress) error {
	var todo []int
	for i, t := range s.Glossary {
		if t.Translation == "" && !t.Off {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		s.log("В словаре всё уже переведено")
		return nil
	}
	s.log("Перевожу словарь: %d терминов", len(todo))
	for start := 0; start < len(todo); start += translate.TermBatch {
		end := min(start+translate.TermBatch, len(todo))
		progress("Перевод словаря", float64(start), float64(len(todo)))
		batch := make([]translate.Term, 0, end-start)
		for _, i := range todo[start:end] {
			batch = append(batch, translate.Term{Source: s.Glossary[i].Source, Example: s.Glossary[i].Example})
		}
		res, err := withRetries(ctx, func() ([]string, error) { return translate.TranslateTerms(ctx, tr, batch) },
			func(wait time.Duration, err error) {
				s.log("Словарь %d–%d: %v — повторю через %s", start+1, end, err, wait)
			})
		if err != nil {
			return fmt.Errorf("словарь %d–%d: %w", start+1, end, err)
		}
		for j, i := range todo[start:end] {
			if j < len(res) {
				s.Glossary[i].Translation = strings.TrimSpace(res[j])
			}
		}
		if err := s.SaveGlossary(to); err != nil {
			return err
		}
	}
	progress("Перевод словаря", float64(len(todo)), float64(len(todo)))
	s.log("Словарь переведён. Проверь его в «Открыть словарь» — исправленное там переводчик возьмёт во все фразы")
	return nil
}

// GlossaryTerms — словарь для переводчика: переведённые статьи без отметки «не подсказывать».
func (s *Session) GlossaryTerms() []translate.Term {
	var out []translate.Term
	for _, t := range s.Glossary {
		if t.Translation != "" && !t.Off {
			out = append(out, translate.Term{Source: t.Source, Translation: t.Translation})
		}
	}
	return out
}
