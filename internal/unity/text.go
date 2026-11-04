package unity

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Текст для перевода в сериализованном файле Unity: таблицы I2 Localization (MonoBehaviour)
// и таблицы CSV в TextAsset, где первая строка — «ключ, en, fr, ru…».

// Entry — строка таблицы локализации на одном языке.
type Entry struct {
	Key  string // «<PathID объекта>/<термин>» — одинаков для всех языков строки
	Lang string // код языка: en-US, fr, es-MX…
	Text string

	obj  int  // индекс объекта в File.Objects
	span Span // I2: место строки в данных объекта
	cell cell // CSV: место ячейки в тексте
	csv  bool
}

// ID — по нему переводы находят свою строку.
func (e Entry) ID() string { return e.Key + "@" + e.Lang }

// Entries — все строки таблиц локализации файла.
func Entries(f *File) []Entry {
	var out []Entry
	for i, o := range f.Objects {
		switch o.ClassID {
		case ClassMonoBehaviour:
			if o.Size < 512 {
				continue
			}
			h, ok := f.Mono(o)
			if !ok {
				continue
			}
			d := f.Data(o)
			src, ok := ParseI2(d, h.Fields, f.Order())
			if !ok {
				continue
			}
			seen := map[string]int{}
			for _, t := range src.Terms {
				key := uniqueKey(seen, fmt.Sprintf("%d/%s", o.PathID, t.Term))
				for c, sp := range t.Values {
					if c >= len(src.Languages) {
						break
					}
					lang := NormLang(src.Languages[c].Code)
					if lang == "" || sp.Len == 0 {
						continue
					}
					out = append(out, Entry{Key: key, Lang: lang, Text: sp.Text(d), obj: i, span: sp})
				}
			}
		case ClassTextAsset:
			_, sp, ok := f.TextScript(o)
			if !ok || sp.Len < 16 {
				continue
			}
			d := f.Data(o)
			text := d[sp.Off+4 : sp.Off+4+sp.Len]
			rows, langs := parseLocCSV(text)
			if rows == nil {
				continue
			}
			seen := map[string]int{}
			for _, row := range rows[1:] {
				if len(row) == 0 || row[0].text == "" {
					continue
				}
				key := uniqueKey(seen, fmt.Sprintf("%d/%s", o.PathID, row[0].text))
				for c := 1; c < len(row) && c < len(langs); c++ {
					if langs[c] == "" || row[c].text == "" {
						continue
					}
					out = append(out, Entry{Key: key, Lang: langs[c], Text: row[c].text, obj: i, cell: row[c], csv: true})
				}
			}
		}
	}
	return out
}

func uniqueKey(seen map[string]int, key string) string {
	seen[key]++
	if n := seen[key]; n > 1 {
		return key + "#" + strconv.Itoa(n)
	}
	return key
}

// headerLang — язык колонки таблицы: код (en_US) или название (ENGLISH, FRENCH CANADIAN — у LEGO).
func headerLang(h string) string {
	if l := NormLang(h); l != "" {
		return l
	}
	return langNames[strings.ToUpper(strings.TrimSpace(h))]
}

var langNames = map[string]string{
	"ENGLISH": "en-GB", "AMERICAN": "en-US", "FRENCH": "fr", "GERMAN": "de", "SPANISH": "es", "ITALIAN": "it",
	"DANISH": "da", "JAPANESE": "ja", "RUSSIAN": "ru", "POLISH": "pl", "FRENCH CANADIAN": "fr-CA",
	"BR PORTUGUESE": "pt-BR", "PORTUGUESE": "pt", "DUTCH": "nl", "KOREAN": "ko", "MEXICAN": "es-MX",
	"CHINESE TRADITIONAL": "zh-TW", "CHINESE SIMPLIFIED": "zh-CN", "ARABIC": "ar", "SWEDISH": "sv",
	"NORWEGIAN": "nb", "FINNISH": "fi", "TURKISH": "tr", "CZECH": "cs",
}

// CSVText — строка таблицы локализации CSV на одном языке.
type CSVText struct {
	Key, Lang, Text string
}

// ID — по нему перевод находит строку.
func (t CSVText) ID() string { return t.Key + "@" + t.Lang }

// CSVTexts — строки таблицы локализации CSV (первая строка — «ключ, язык, язык…»); nil — не таблица.
func CSVTexts(b []byte) []CSVText {
	rows, langs := parseLocCSV(b)
	if rows == nil {
		return nil
	}
	var out []CSVText
	seen := map[string]int{}
	for _, row := range rows[1:] {
		if len(row) == 0 || row[0].text == "" {
			continue
		}
		key := uniqueKey(seen, row[0].text)
		for c := 1; c < len(row) && c < len(langs); c++ {
			if langs[c] != "" && row[c].text != "" {
				out = append(out, CSVText{key, langs[c], row[c].text})
			}
		}
	}
	return out
}

// ReplaceCSV вписывает переводы (CSVText.ID → текст) в таблицу CSV.
func ReplaceCSV(b []byte, tr map[string]string) ([]byte, int) {
	rows, langs := parseLocCSV(b)
	if rows == nil {
		return nil, 0
	}
	var cs []cell
	repl := map[int]string{}
	seen := map[string]int{}
	for _, row := range rows[1:] {
		if len(row) == 0 || row[0].text == "" {
			continue
		}
		key := uniqueKey(seen, row[0].text)
		for c := 1; c < len(row) && c < len(langs); c++ {
			if langs[c] == "" {
				continue
			}
			if t, ok := tr[key+"@"+langs[c]]; ok && t != row[c].text {
				cs = append(cs, row[c])
				repl[row[c].start] = t
			}
		}
	}
	if len(cs) == 0 {
		return nil, 0
	}
	return replaceCells(b, cs, repl), len(cs)
}

// NormLang приводит код языка к виду «en», «en-US», «zh-CN»; "" — не код языка.
func NormLang(code string) string {
	code = strings.TrimSpace(strings.ReplaceAll(code, "_", "-"))
	if !langCodeRe.MatchString(code) {
		return ""
	}
	lang, region, ok := strings.Cut(code, "-")
	lang = strings.ToLower(lang)
	if !ok {
		return lang
	}
	if len(region) == 2 || region[0] >= '0' && region[0] <= '9' {
		region = strings.ToUpper(region)
	} else {
		region = strings.ToUpper(region[:1]) + strings.ToLower(region[1:]) // Hans, Hant
	}
	return lang + "-" + region
}

// Apply вписывает переводы (Entry.ID → текст) и возвращает новый файл и число вписанных строк.
func Apply(f *File, tr map[string]string) ([]byte, int, error) {
	entries := Entries(f)
	strs := map[int]map[int]string{}  // объект → место строки → текст (I2)
	cells := map[int]map[int]string{} // объект → начало ячейки → текст (CSV)
	csvCells := map[int][]cell{}
	n := 0
	for _, e := range entries {
		t, ok := tr[e.ID()]
		if !ok || t == e.Text {
			continue
		}
		if e.csv {
			if cells[e.obj] == nil {
				cells[e.obj] = map[int]string{}
			}
			cells[e.obj][e.cell.start] = t
			csvCells[e.obj] = append(csvCells[e.obj], e.cell)
		} else {
			if strs[e.obj] == nil {
				strs[e.obj] = map[int]string{}
			}
			strs[e.obj][e.span.Off] = t
		}
		n++
	}
	if n == 0 {
		return nil, 0, nil
	}
	changed := map[int][]byte{}
	for obj, repl := range strs {
		changed[obj] = ReplaceStrings(f.Data(f.Objects[obj]), f.Order(), repl)
	}
	for obj, repl := range cells {
		d := f.Data(f.Objects[obj])
		_, sp, _ := f.TextScript(f.Objects[obj])
		text := d[sp.Off+4 : sp.Off+4+sp.Len]
		changed[obj] = ReplaceStrings(d, f.Order(), map[int]string{sp.Off: string(replaceCells(text, csvCells[obj], repl))})
	}
	out, err := f.Rebuild(changed)
	return out, n, err
}

// cell — ячейка CSV: текст и где она в исходном тексте (с кавычками).
type cell struct {
	text       string
	start, end int
	quoted     bool
}

// parseLocCSV разбирает CSV, если это таблица локализации: в первой строке после первой ячейки —
// коды языков (не меньше двух). langs — нормализованный код языка каждой колонки ("" — не язык).
func parseLocCSV(b []byte) ([][]cell, []string) {
	start := 0
	if bytes.HasPrefix(b, []byte("\xEF\xBB\xBF")) {
		start = 3
	}
	nl := bytes.IndexAny(b[start:], "\r\n")
	if nl < 0 || bytes.IndexByte(b[start:start+nl], ',') < 0 {
		return nil, nil
	}
	rows := parseCSV(b, start)
	if len(rows) < 2 {
		return nil, nil
	}
	langs := make([]string, len(rows[0]))
	found := 0
	for i, c := range rows[0] {
		if i == 0 {
			continue
		}
		if langs[i] = headerLang(c.text); langs[i] != "" {
			found++
		}
	}
	if found < 2 {
		return nil, nil
	}
	return rows, langs
}

// parseCSV — строки CSV с кавычками "…" (внутри "" — кавычка, переводы строк разрешены).
func parseCSV(b []byte, p int) [][]cell {
	var rows [][]cell
	var row []cell
	for p <= len(b) {
		var c cell
		c.start = p
		if p < len(b) && b[p] == '"' {
			c.quoted = true
			var sb strings.Builder
			p++
			for p < len(b) {
				if b[p] == '"' {
					if p+1 < len(b) && b[p+1] == '"' {
						sb.WriteByte('"')
						p += 2
						continue
					}
					p++
					break
				}
				sb.WriteByte(b[p])
				p++
			}
			c.text = sb.String()
			for p < len(b) && b[p] != ',' && b[p] != '\n' && b[p] != '\r' {
				p++
			}
		} else {
			q := p
			for p < len(b) && b[p] != ',' && b[p] != '\n' && b[p] != '\r' {
				p++
			}
			c.text = string(b[q:p])
		}
		c.end = p
		row = append(row, c)
		if p >= len(b) {
			rows = append(rows, row)
			break
		}
		switch b[p] {
		case ',':
			p++
		case '\r', '\n':
			if b[p] == '\r' && p+1 < len(b) && b[p+1] == '\n' {
				p++
			}
			p++
			rows = append(rows, row)
			row = nil
			if p >= len(b) {
				return rows
			}
		}
	}
	return rows
}

// replaceCells — текст CSV с новыми ячейками (начало ячейки → текст).
func replaceCells(b []byte, cells []cell, repl map[int]string) []byte {
	sortCells(cells)
	var out []byte
	prev := 0
	for _, c := range cells {
		if c.start < prev {
			continue
		}
		out = append(out, b[prev:c.start]...)
		t := repl[c.start]
		if c.quoted || strings.ContainsAny(t, ",\"\r\n") {
			out = append(out, '"')
			out = append(out, strings.ReplaceAll(t, `"`, `""`)...)
			out = append(out, '"')
		} else {
			out = append(out, t...)
		}
		prev = c.end
	}
	return append(out, b[prev:]...)
}

func sortCells(cs []cell) { slices.SortFunc(cs, func(a, b cell) int { return a.start - b.start }) }
