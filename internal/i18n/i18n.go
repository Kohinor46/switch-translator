// Package i18n — языки интерфейса. Ключ перевода — сама русская строка из исходников
// (русский — язык разработки, файла для него нет), переводы — locales/<язык>.json.
//
// T переводит строку из кода; Msg — уже готовое сообщение (строку лога, текст ошибки):
// оно сравнивается с шаблонами ключей с %d, %s, %v…, подставленные части переводятся тоже.
// Список ключей и проверка файлов переводов — go run ./tools/i18n.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed locales/*.json
var files embed.FS

// Language — язык интерфейса; Name — на самом этом языке.
type Language struct {
	Code string
	Name string
}

// Languages — в порядке списка в настройках.
var Languages = []Language{
	{"en", "English"},
	{"ru", "Русский"},
	{"es", "Español"},
	{"it", "Italiano"},
	{"de", "Deutsch"},
	{"fr", "Français"},
	{"pt", "Português"},
	{"zh", "中文"},
	{"ja", "日本語"},
	{"hi", "हिन्दी"},
	{"ar", "العربية"},
}

const source = "ru"

var (
	mu      sync.RWMutex
	current = source
	strs    map[string]string
)

// Set включает язык; неизвестный или непрочитанный — русский.
func Set(code string) {
	m, err := load(code)
	if err != nil || code == source {
		code, m = source, nil
	}
	mu.Lock()
	current, strs = code, m
	mu.Unlock()
}

// Current — код включённого языка.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

func load(code string) (map[string]string, error) {
	data, err := files.ReadFile("locales/" + code + ".json")
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("locales/%s.json: %w", code, err)
	}
	return m, nil
}

// Match — язык программы для языка системы вида «pt-BR», «zh_Hans_CN», «ru_RU.UTF-8».
// Если такого нет — английский.
func Match(locale string) string {
	parts := strings.FieldsFunc(locale, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	if len(parts) > 0 {
		base := strings.ToLower(parts[0])
		for _, l := range Languages {
			if l.Code == base {
				return l.Code
			}
		}
	}
	return "en"
}

// T — перевод строки key; с args — как fmt.Sprintf.
func T(key string, args ...any) string {
	mu.RLock()
	s, ok := strs[key]
	mu.RUnlock()
	if !ok {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Err — текст ошибки на языке интерфейса.
func Err(err error) string {
	if err == nil {
		return ""
	}
	return Msg(err.Error())
}

// Msg — перевод готового сообщения. Сообщения из нескольких частей через «: » (обёрнутые
// ошибки) переводятся по частям.
func Msg(s string) string {
	mu.RLock()
	m := strs
	mu.RUnlock()
	if m == nil || s == "" {
		return s
	}
	return msg(m, s, 0)
}

func msg(m map[string]string, s string, depth int) string {
	if v, ok := m[s]; ok && !strings.Contains(s, "%") {
		return v
	}
	if depth > 3 {
		return s
	}
	if v, ok := matchPattern(m, s, depth); ok {
		return v
	}
	// «часть: часть: часть» — каждая часть отдельно
	if parts := strings.Split(s, ": "); len(parts) > 1 {
		changed := false
		for i, p := range parts {
			if t := msg(m, p, depth+1); t != p {
				parts[i] = t
				changed = true
			}
		}
		if changed {
			return strings.Join(parts, ": ")
		}
	}
	return s
}

// Шаблоны: ключ с глаголами формата → регулярное выражение, подставленные значения — группы.

type pattern struct {
	key    string
	prefix string // текст до первого глагола: быстрый отсев
	re     *regexp.Regexp
	verbs  []byte // буква каждого глагола
}

var (
	verbRe       = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[dsvqwxXfgeEtTcbo%]`)
	patternsOnce sync.Once
	patterns     []pattern
)

func compilePatterns() {
	data, err := files.ReadFile("locales/en.json")
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return
	}
	for key := range m {
		locs := verbRe.FindAllStringIndex(key, -1)
		if len(locs) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("^")
		var verbs []byte
		last := 0
		real := 0
		for _, l := range locs {
			b.WriteString(regexp.QuoteMeta(key[last:l[0]]))
			v := key[l[1]-1]
			switch v {
			case '%':
				b.WriteString("%")
			case 'd':
				b.WriteString(`(-?\d+)`)
				real++
			case 'f', 'g', 'e':
				b.WriteString(`(-?[\d.,]+)`)
				real++
			default:
				b.WriteString(`(.+?)`)
				real++
			}
			if v != '%' {
				verbs = append(verbs, v)
			}
			last = l[1]
		}
		if real == 0 {
			continue
		}
		b.WriteString(regexp.QuoteMeta(key[last:]))
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			continue
		}
		patterns = append(patterns, pattern{key: key, prefix: key[:locs[0][0]], re: re, verbs: verbs})
	}
	// длинные шаблоны точнее: проверяются первыми
	sortPatterns(patterns)
}

func sortPatterns(p []pattern) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && len(p[j].key) > len(p[j-1].key); j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

func matchPattern(m map[string]string, s string, depth int) (string, bool) {
	patternsOnce.Do(compilePatterns)
	for _, p := range patterns {
		if !strings.HasPrefix(s, p.prefix) {
			continue
		}
		g := p.re.FindStringSubmatch(s)
		if g == nil {
			continue
		}
		tr, ok := m[p.key]
		if !ok {
			continue
		}
		vals := g[1:]
		for i, v := range vals {
			if i < len(p.verbs) && p.verbs[i] != 'd' && p.verbs[i] != 'f' {
				vals[i] = msg(m, v, depth+1)
			}
		}
		// глаголы перевода идут в том же порядке, что и в ключе
		i := 0
		out := verbRe.ReplaceAllStringFunc(tr, func(v string) string {
			if v == "%%" {
				return "%"
			}
			if i >= len(vals) {
				return v
			}
			i++
			return vals[i-1]
		})
		return out, true
	}
	return "", false
}

// Phrases — строка, склеенная из известных кусков (название формата «SARC (архив Nintendo) [Yaz0] с MSBT»):
// сначала как целое сообщение, иначе каждый известный кусок с русскими буквами заменяется переводом.
func Phrases(s string) string {
	mu.RLock()
	m := strs
	mu.RUnlock()
	if m == nil || s == "" {
		return s
	}
	if t := msg(m, s, 0); t != s {
		return t
	}
	phrasesOnce.Do(func() {
		for k := range m {
			if !strings.Contains(k, "%") && utf8.RuneCountInString(strings.TrimSpace(k)) >= 3 && hasCyrillic(k) {
				phraseKeys = append(phraseKeys, k)
			}
		}
		sort.Slice(phraseKeys, func(i, j int) bool { return len(phraseKeys[i]) > len(phraseKeys[j]) })
	})
	for _, k := range phraseKeys {
		if !hasCyrillic(s) {
			break
		}
		if strings.Contains(s, k) {
			s = strings.ReplaceAll(s, k, m[k])
		}
	}
	return s
}

var (
	phrasesOnce sync.Once
	phraseKeys  []string
)

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
