package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Словарь игры: имена, места, предметы и приёмы переводятся один раз, а потом ИИ-переводчик
// получает с каждой пачкой фраз те статьи словаря, что в ней встречаются, — и переводит термины
// везде одинаково. Машинные переводчики (DeepL, Google…) словарь не принимают.

// Term — статья словаря.
type Term struct {
	Source      string
	Translation string
	Example     string // фраза из игры с этим словом — подсказка, в каком смысле оно
}

// TermTranslator переводит статьи словаря как термины (именительный падеж, имена — устоявшейся транскрипцией).
type TermTranslator interface {
	TranslateTerms(ctx context.Context, terms []Term) ([]string, error)
}

// TranslateTerms переводит статьи словаря: ИИ — с пояснением, что это термины, остальные — как обычные строки.
func TranslateTerms(ctx context.Context, tr Translator, terms []Term) ([]string, error) {
	if t, ok := tr.(TermTranslator); ok {
		return t.TranslateTerms(ctx, terms)
	}
	src := make([]string, len(terms))
	for i, t := range terms {
		src[i] = t.Source
	}
	return tr.Translate(ctx, src)
}

// TermBatch — сколько статей словаря отдавать за раз.
const TermBatch = 150

func (l *llm) TranslateTerms(ctx context.Context, terms []Term) ([]string, error) {
	type item struct {
		Term    string `json:"term"`
		Example string `json:"example,omitempty"`
	}
	in := make([]item, len(terms))
	for i, t := range terms {
		in[i] = item{t.Source, t.Example}
	}
	b, _ := json.Marshal(in)
	system := fmt.Sprintf("You are a professional video game localizer building a glossary from %s to %s.\n",
		englishName(l.cfg.From), englishName(l.cfg.To)) + `Each item is a term from the game (a name, place, item, move, menu word) with an example line where it appears.
Rules:
- Give the term the way an official localization would: character and place names in the conventional transliteration, item and move names translated, brand names and button names (A, ZL…) unchanged.
- Use the dictionary form (nominative, singular unless the term is plural).
- Translate only the term, not the example.
- Return only a JSON object {"translations": [...]} with exactly as many strings as the input, in the same order.
`
	if c := strings.TrimSpace(l.cfg.Context); c != "" {
		system += "\nAbout the game:\n" + c + "\n"
	}
	out, err := l.call(ctx, l.cfg, system, fmt.Sprintf("Glossary terms (%d):\n%s", len(terms), b))
	if err != nil {
		return nil, err
	}
	return parseTranslations(out, len(terms))
}

// glossaryFor — статьи словаря, которые встречаются в этих строках.
func glossaryFor(g []Term, texts []string) []Term {
	if len(g) == 0 {
		return nil
	}
	hay := strings.ToLower(strings.Join(texts, "\n"))
	var out []Term
	for _, t := range g {
		if t.Translation != "" && containsWord(hay, strings.ToLower(t.Source)) {
			out = append(out, t)
		}
	}
	return out
}

// containsWord — есть ли слово целиком (для языков без пробелов — просто подстрока).
func containsWord(hay, w string) bool {
	if w == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(hay[from:], w)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(w)
		if !wordRune(lastRune(hay[:i])) && !wordRune(firstRune(hay[end:])) || !spaced(w) {
			return true
		}
		from = i + 1
	}
}

// spaced — в письменности слова отделяются пробелами (не китайский и не японский).
func spaced(w string) bool {
	for _, r := range w {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return false
		}
	}
	return true
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	r := rune(0)
	for _, c := range s {
		r = c
	}
	return r
}

var glossaryLineRe = regexp.MustCompile(`[\r\n]+`)

// glossaryPrompt — статьи словаря для пачки: «Toad → Тоад».
func glossaryPrompt(g []Term, to string) string {
	if len(g) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Glossary — use these translations of game terms (inflect them as %s grammar requires, keep the stem):\n", englishName(to))
	for _, t := range g {
		fmt.Fprintf(&sb, "%s → %s\n", glossaryLineRe.ReplaceAllString(t.Source, " "), glossaryLineRe.ReplaceAllString(t.Translation, " "))
	}
	return sb.String() + "\n"
}
