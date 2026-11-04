// Package gametext достаёт строки для перевода из игровых форматов.
package gametext

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"switch-translator/internal/codec"
	"switch-translator/internal/unity"
)

// String — одна реплика. Теги игры записаны как {{...}} и при переводе
// должны остаться на месте.
type String struct {
	ID          string `json:"id"`
	File        string `json:"file"`
	Inner       string `json:"inner,omitempty"`
	Label       string `json:"label"`
	Index       int    `json:"index"`
	Locale      string `json:"locale,omitempty"` // USen, EUde, JPja… — из пути к файлу
	Source      string `json:"source"`
	Translation string `json:"translation,omitempty"`
	// Skip — не переводить: пользователь снял галочку или переводить нечего (только коды и цифры).
	Skip bool `json:"skip,omitempty"`
}

// HasLetters — есть ли в реплике что переводить, кроме управляющих кодов, цифр и знаков.
func HasLetters(s string) bool {
	for _, r := range codeRe.ReplaceAllString(s, "") {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

var codeRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

type sarcFile struct {
	Name string
	Data []byte
}

func byteOrder(bom []byte) binary.ByteOrder {
	if bom[0] == 0xFE && bom[1] == 0xFF {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

func parseSARC(data []byte) ([]sarcFile, error) {
	if len(data) < 0x14 || string(data[:4]) != "SARC" {
		return nil, fmt.Errorf("не SARC")
	}
	bo := byteOrder(data[6:8])
	hdrLen := int(bo.Uint16(data[4:]))
	dataOff := int(bo.Uint32(data[0xC:]))
	if hdrLen+0xC > len(data) || string(data[hdrLen:hdrLen+4]) != "SFAT" {
		return nil, fmt.Errorf("SARC без SFAT")
	}
	sfat := data[hdrLen:]
	count := int(bo.Uint16(sfat[6:]))
	nodes := sfat[0xC:]
	sfntPos := hdrLen + 0xC + count*0x10
	if sfntPos+8 > len(data) || string(data[sfntPos:sfntPos+4]) != "SFNT" {
		return nil, fmt.Errorf("SARC без SFNT")
	}
	names := data[sfntPos+8:]

	out := make([]sarcFile, 0, count)
	for i := 0; i < count; i++ {
		n := nodes[i*0x10:]
		attr := bo.Uint32(n[4:])
		start, end := dataOff+int(bo.Uint32(n[8:])), dataOff+int(bo.Uint32(n[12:]))
		if start > end || end > len(data) {
			return nil, fmt.Errorf("SARC: файл %d за пределами архива", i)
		}
		name := fmt.Sprintf("file_%04d", i)
		if attr>>24 != 0 {
			if off := int(attr&0xFFFF) * 4; off < len(names) {
				s := names[off:]
				if z := bytes.IndexByte(s, 0); z >= 0 {
					name = string(s[:z])
				}
			}
		}
		out = append(out, sarcFile{name, data[start:end]})
	}
	return out, nil
}

type msbtMessage struct {
	Label string
	Index int
	Text  string
}

func parseMSBT(data []byte) ([]msbtMessage, error) {
	if len(data) < 0x20 || string(data[:8]) != "MsgStdBn" {
		return nil, fmt.Errorf("не MSBT")
	}
	bo := byteOrder(data[8:10])
	enc := data[0xC]
	nsec := int(bo.Uint16(data[0xE:]))

	labels := map[int]string{}
	var texts []string
	pos := 0x20
	for s := 0; s < nsec && pos+0x10 <= len(data); s++ {
		magic := sectionMagic(data[pos : pos+4])
		size := int(bo.Uint32(data[pos+4:]))
		body := pos + 0x10
		if body+size > len(data) {
			return nil, fmt.Errorf("MSBT: секция %s обрезана", magic)
		}
		sec := data[body : body+size]
		switch magic {
		case "LBL1":
			groups := int(bo.Uint32(sec))
			for g := 0; g < groups && 4+g*8+8 <= len(sec); g++ {
				cnt := int(bo.Uint32(sec[4+g*8:]))
				off := int(bo.Uint32(sec[8+g*8:]))
				for j := 0; j < cnt && off < len(sec); j++ {
					l := int(sec[off])
					if off+1+l+4 > len(sec) {
						break
					}
					labels[int(bo.Uint32(sec[off+1+l:]))] = string(sec[off+1 : off+1+l])
					off += 1 + l + 4
				}
			}
		case "TXT2":
			cnt := int(bo.Uint32(sec))
			for i := 0; i < cnt; i++ {
				start := int(bo.Uint32(sec[4+i*4:]))
				end := len(sec)
				if i+1 < cnt {
					end = int(bo.Uint32(sec[4+(i+1)*4:]))
				}
				if start > end || end > len(sec) {
					return nil, fmt.Errorf("MSBT: строка %d за пределами секции", i)
				}
				texts = append(texts, decodeLMS(sec[start:end], enc, bo))
			}
		}
		pos = (body + size + 15) &^ 15
	}

	out := make([]msbtMessage, len(texts))
	for i, t := range texts {
		label, ok := labels[i]
		if !ok {
			label = fmt.Sprintf("#%d", i)
		}
		out[i] = msbtMessage{label, i, t}
	}
	return out, nil
}

// sectionMagic — имя секции LMS; в портах вроде Super Mario Galaxy 2 оно записано задом наперёд («1LBL»).
func sectionMagic(b []byte) string {
	m := string(b)
	if len(m) == 4 && m[0] >= '0' && m[0] <= '9' {
		return string([]byte{m[3], m[2], m[1], m[0]})
	}
	return m
}

// decodeLMS превращает строку LMS в текст с тегами вида {{группа:тип:параметры}} и {{/группа:тип}}.
func decodeLMS(b []byte, enc byte, bo binary.ByteOrder) string {
	var sb strings.Builder
	if enc != 1 { // UTF-8 (0) — без разбора тегов; UTF-32 в играх Switch не встречался
		return strings.TrimRight(string(b), "\x00")
	}
	u16 := func(i int) uint16 {
		if i+2 > len(b) {
			return 0
		}
		return bo.Uint16(b[i:])
	}
	var run []uint16
	flush := func() {
		sb.WriteString(string(utf16.Decode(run)))
		run = run[:0]
	}
	for i := 0; i+2 <= len(b); {
		c := u16(i)
		i += 2
		switch c {
		case 0:
			flush()
			return sb.String()
		case 0x0E:
			flush()
			group, typ, size := u16(i), u16(i+2), int(u16(i+4))
			i += 6
			end := min(i+size, len(b))
			fmt.Fprintf(&sb, "{{%d:%d:%s}}", group, typ, hex.EncodeToString(b[i:end]))
			i = end
		case 0x0F:
			flush()
			fmt.Fprintf(&sb, "{{/%d:%d}}", u16(i), u16(i+2))
			i += 4
		default:
			run = append(run, c)
		}
	}
	flush()
	return sb.String()
}

// Extract достаёт строки из файла RomFS. Поддерживает MSBT, в том числе внутри SARC и Yaz0/zstd.
// Для форматов без поддержки возвращает nil, nil.
func Extract(file string, data []byte) ([]String, error) {
	data, _, err := codec.Unwrap(data)
	if err != nil {
		return nil, err
	}
	switch {
	case unity.IsSerialized(data):
		return fromUnity(file, data)
	case isRARC(data):
		return fromRARC(file, data)
	case IsLocres(data):
		return FromLocres(file, "", data, nil)
	case bytes.HasPrefix(data, []byte("MsgStdBn")):
		return fromMSBT(file, "", data)
	case bytes.HasPrefix(data, []byte("SARC")):
		files, err := parseSARC(data)
		if err != nil {
			return nil, err
		}
		var out []String
		for _, f := range files {
			inner, _, err := codec.Unwrap(f.Data)
			if err != nil || !bytes.HasPrefix(inner, []byte("MsgStdBn")) {
				continue
			}
			s, err := fromMSBT(file, f.Name, inner)
			if err != nil {
				return out, fmt.Errorf("%s/%s: %w", file, f.Name, err)
			}
			out = append(out, s...)
		}
		return out, nil
	}
	return nil, nil
}

func fromMSBT(file, inner string, data []byte) ([]String, error) {
	msgs, err := parseMSBT(data)
	if err != nil {
		return nil, err
	}
	loc := Locale(file)
	if loc == "" {
		loc = Locale(inner)
	}
	out := make([]String, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		out = append(out, String{
			ID:     path.Join(file, inner) + "#" + m.Label,
			File:   file,
			Inner:  inner,
			Label:  m.Label,
			Index:  m.Index,
			Locale: loc,
			Source: m.Text,
			Skip:   !HasLetters(m.Text),
		})
	}
	return out, nil
}

// Игры Nintendo хранят все языки сразу: LocalizedData/EUde/…, Mals/USen.Product.sarc.zs, Message_JPja…,
// а также LocalizedData/EuDe/… (Bowser's Fury) и msg/EU_Dutch/…, msg/Kirby15/CN_Chinese/… (Paper Mario, Kirby),
// LuigiBros/msg/EUR_de/… (Bowser's Fury). NSen в Bowser's Fury — только английский, общий для всех: остаётся без кода.
var (
	localeRe      = regexp.MustCompile(`^(US|EU|JP|KR|CN|TW|AU|TH)([a-z]{2})$`)
	localeTitleRe = regexp.MustCompile(`^(Us|Eu|Jp|Kr|Cn|Tw|Au|Th)([A-Z][a-z])$`)
	localeWiiRe   = regexp.MustCompile(`^(Us|Eu|Jp|Kr|Cn|As)([A-Z][A-Za-z]+)$`)      // UsEnglish, AsTradChinese (Super Mario Galaxy)
	localeLongRe  = regexp.MustCompile(`^(USA|EUR|JPN|KOR|CHN|TWN|AUS)_([a-z]{2})$`) // LuigiBros/msg/EUR_de (Bowser's Fury)
	localeNameRe  = regexp.MustCompile(`^(US|EU|JP|KR|CN|TW|AU|TH)_([A-Za-z]+)(_\w+)?$`)
)

// localeNames — названия языков в папках вида EU_Dutch.
var localeNames = map[string]string{
	"english": "en", "french": "fr", "german": "de", "italian": "it", "spanish": "es", "dutch": "nl",
	"portuguese": "pt", "russian": "ru", "polish": "pl", "japanese": "ja", "korean": "ko", "chinese": "zh",
	"simpchinese": "zh", "tradchinese": "zh", "simplifiedchinese": "zh", "traditionalchinese": "zh", "thai": "th",
}

// Locale находит код региона и языка в пути: "USen", "EUde" или "".
func Locale(p string) string {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '.' }) {
		if m := localeWiiRe.FindStringSubmatch(seg); m != nil {
			if lang, ok := localeNames[strings.ToLower(m[2])]; ok {
				region := strings.ToUpper(m[1])
				if region == "AS" {
					region = "TW"
				}
				return region + lang
			}
		}
		if m := localeLongRe.FindStringSubmatch(seg); m != nil {
			return m[1][:2] + m[2]
		}
		if m := localeNameRe.FindStringSubmatch(seg); m != nil {
			if lang, ok := localeNames[strings.ToLower(m[2])]; ok {
				return m[1] + lang
			}
		}
		for _, part := range strings.FieldsFunc(seg, func(r rune) bool { return r == '_' || r == '-' }) {
			if localeRe.MatchString(part) {
				return part
			}
			if m := localeTitleRe.FindStringSubmatch(part); m != nil {
				return strings.ToUpper(m[1]) + strings.ToLower(m[2])
			}
		}
	}
	return ""
}

// LocaleLang — код языка из кода локали: "USen" → "en"; у таблиц Unity коды обычные: "en-US" → "en", "ja" → "ja".
func LocaleLang(loc string) string {
	if lang, _, ok := strings.Cut(loc, "-"); ok {
		return strings.ToLower(lang)
	}
	if len(loc) < 2 {
		return ""
	}
	return strings.ToLower(loc[len(loc)-2:])
}

// MayContainText — стоит ли вообще читать файл целиком в поисках строк.
func MayContainText(format string) bool {
	return strings.HasPrefix(format, "MSBT") || strings.HasPrefix(format, "SARC") || strings.HasPrefix(format, "Unity serialized") || strings.HasPrefix(format, "RARC") || strings.HasPrefix(format, "Unreal .locres")
}

// StripCodes убирает служебные коды {{…}} — остаётся текст, который видно на экране.
func StripCodes(s string) string { return codeRe.ReplaceAllString(s, "") }
