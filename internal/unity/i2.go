package unity

import (
	"encoding/binary"
	"regexp"
	"slices"
	"unicode/utf8"
)

// I2 Localization: MonoBehaviour I2.Loc.LanguageSource / LanguageSourceAsset хранит список терминов,
// у каждого — перевод на каждый язык игры, и список языков. Дерева типов в сборках для Switch нет,
// а поля перед терминами и внутри терминов разнятся от версии к версии плагина, поэтому список
// терминов ищется перебором: он должен целиком разобраться и за ним должен идти список языков той же длины.

// Span — строка Unity внутри данных объекта: Off — где её длина (int32), Len — сколько байт текста.
type Span struct{ Off, Len int }

// I2Lang — язык таблицы.
type I2Lang struct{ Name, Code string }

// I2Term — термин и его переводы по языкам (по порядку Languages).
type I2Term struct {
	Term   string
	Values []Span
}

// I2Source — разобранная таблица.
type I2Source struct {
	Languages []I2Lang
	Terms     []I2Term
}

// i2Variant — как устроен термин: есть ли Description и что идёт после переводов:
// b — массив байт (Flags), s — массив строк (старое Languages_Touch).
type i2Variant struct {
	desc bool
	tail string
}

var i2Variants = func() []i2Variant {
	var out []i2Variant
	for _, tail := range []string{"b", "sb", "bs", "bb", ""} {
		for _, desc := range []bool{true, false} {
			out = append(out, i2Variant{desc, tail})
		}
	}
	return out
}()

// ParseI2 ищет таблицу I2 в данных MonoBehaviour (from — начало полей скрипта).
func ParseI2(b []byte, from int, ord binary.ByteOrder) (*I2Source, bool) {
	for p := from; p+8 <= len(b) && p < from+16384; p += 4 {
		n := int(int32(ord.Uint32(b[p:])))
		if n < 2 || n > 1<<20 || n*12 > len(b)-p {
			continue
		}
		for _, v := range i2Variants {
			terms, end, cols, ok := parseI2Terms(b, p+4, n, ord, v)
			if !ok {
				continue
			}
			if langs, ok := findI2Langs(b, end, cols, ord); ok {
				return &I2Source{Languages: langs, Terms: terms}, true
			}
		}
	}
	return nil, false
}

// parseI2Terms разбирает n терминов; cols — наибольшее число переводов у термина
// (у терминов, добавленных до новых языков, переводов меньше).
func parseI2Terms(b []byte, p, n int, ord binary.ByteOrder, v i2Variant) ([]I2Term, int, int, bool) {
	s := &ostream{b: b, p: p, ord: ord}
	terms := make([]I2Term, 0, min(n, 1<<16))
	cols := 0
	array := func(kind byte) bool {
		k := int(s.i32())
		if s.bad || k < 0 || k > 256 {
			return false
		}
		if kind == 'b' {
			if s.p+k > len(b) {
				return false
			}
			s.p += k
			s.align()
			return true
		}
		for range k {
			if s.str(); s.bad {
				return false
			}
		}
		return true
	}
	for range n {
		var t I2Term
		t.Term, _ = s.span()
		if s.bad || !utf8.ValidString(t.Term) {
			return nil, 0, 0, false
		}
		if tt := s.i32(); tt < 0 || tt > 32 {
			return nil, 0, 0, false
		}
		if v.desc {
			if s.str(); s.bad {
				return nil, 0, 0, false
			}
		}
		l := int(s.i32())
		if l < 0 || l > 256 {
			return nil, 0, 0, false
		}
		cols = max(cols, l)
		for range l {
			text, sp := s.span()
			if s.bad || !utf8.ValidString(text) {
				return nil, 0, 0, false
			}
			t.Values = append(t.Values, sp)
		}
		for i := range len(v.tail) {
			if !array(v.tail[i]) {
				return nil, 0, 0, false
			}
		}
		terms = append(terms, t)
	}
	keys := 0
	for _, t := range terms {
		if t.Term != "" {
			keys++
		}
	}
	return terms, s.p, cols, cols > 0 && keys*2 >= len(terms)
}

var langCodeRe = regexp.MustCompile(`^[a-z]{2,3}([-_]([A-Za-z]{2,4}|[0-9]{3}))?$`) // en, en-US, zh-Hans, es-419

// findI2Langs — список языков (Name, Code и, в новых версиях, байт флагов) вскоре после терминов.
func findI2Langs(b []byte, from, cols int, ord binary.ByteOrder) ([]I2Lang, bool) {
	for q := from; q+4 <= len(b) && q <= from+64; q += 4 {
		count := int(int32(ord.Uint32(b[q:])))
		if count < 1 || count < cols-8 || count > cols+16 { // у части терминов переводов бывает больше, чем языков
			continue
		}
		for _, flags := range []bool{true, false} {
			s := &ostream{b: b, p: q + 4, ord: ord}
			var langs []I2Lang
			for range count {
				var l I2Lang
				l.Name, _ = s.span()
				l.Code, _ = s.span()
				if flags {
					s.p++
					s.align()
				}
				if s.bad || len(l.Name) > 64 || !utf8.ValidString(l.Name) || !utf8.ValidString(l.Code) || len(l.Code) > 16 {
					break
				}
				langs = append(langs, l)
			}
			named := false // хоть у одного языка — настоящий код: en, fr-CA…
			for _, l := range langs {
				named = named || langCodeRe.MatchString(l.Code)
			}
			if len(langs) == count && named {
				return langs, true
			}
		}
	}
	return nil, false
}

// span читает строку и запоминает, где она.
func (s *ostream) span() (string, Span) {
	off := s.p
	v := s.str()
	if s.bad {
		return "", Span{}
	}
	return v, Span{off, len(v)}
}

// Text — строка по её месту.
func (sp Span) Text(b []byte) string { return string(b[sp.Off+4 : sp.Off+4+sp.Len]) }

// ReplaceStrings — данные объекта с заменёнными строками (место → новый текст). Строка Unity вместе
// с выравниванием занимает кратное 4 число байт, поэтому всё, что идёт после, остаётся выровненным.
func ReplaceStrings(b []byte, ord binary.ByteOrder, repl map[int]string) []byte {
	if len(repl) == 0 {
		return b
	}
	offs := make([]int, 0, len(repl))
	for off := range repl {
		offs = append(offs, off)
	}
	slices.Sort(offs)
	out := make([]byte, 0, len(b)+len(b)/8)
	prev := 0
	for _, off := range offs {
		n := int(ord.Uint32(b[off:]))
		end := off + 4 + n
		end += (4 - end%4) % 4
		out = append(out, b[prev:off]...)
		text := repl[off]
		out = binary.LittleEndian.AppendUint32(out, 0)
		ord.PutUint32(out[len(out)-4:], uint32(len(text)))
		out = append(out, text...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		prev = end
	}
	return append(out, b[prev:]...)
}
