package gametext

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"golang.org/x/image/font/sfnt"

	"switch-translator/internal/codec"
)

// Font — шрифт игры и символы, которые в нём есть.
type Font struct {
	Name     string // путь к файлу, для архива — «архив/шрифт.bffnt»
	Chars    map[rune]bool
	Scalable bool // векторный (BFTTF/BFOTF, TTF/OTF): буквы в него не дорисовываются
}

// Fonts находит шрифты в файле (в том числе внутри SARC и Yaz0/zstd): растровые BFFNT
// и векторные BFTTF/BFOTF — во многих играх текст рисуется именно ими.
func Fonts(file string, data []byte) ([]Font, error) {
	raw, _, err := codec.Unwrap(data)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(raw, []byte("SARC")) {
		files, err := parseSARC(raw)
		if err != nil {
			return nil, err
		}
		var out []Font
		for _, f := range files {
			inner, _, err := codec.Unwrap(f.Data)
			if err != nil {
				continue
			}
			font, ok, err := parseFont(file+"/"+f.Name, inner)
			if err != nil {
				return out, err
			}
			if ok {
				out = append(out, font)
			}
		}
		return out, nil
	}
	font, ok, err := parseFont(file, raw)
	if !ok || err != nil {
		return nil, err
	}
	return []Font{font}, nil
}

// parseFont читает один шрифт; ok = false, если это не шрифт.
func parseFont(name string, b []byte) (Font, bool, error) {
	if bytes.HasPrefix(b, []byte("FFNT")) {
		chars, err := fontChars(b)
		if err != nil {
			return Font{}, false, fmt.Errorf("%s: %w", name, err)
		}
		return Font{Name: name, Chars: chars}, true, nil
	}
	otf := switchFont(b)
	if otf == nil {
		return Font{}, false, nil
	}
	f, err := sfnt.Parse(otf)
	if err != nil {
		return Font{}, false, fmt.Errorf("%s: %w", name, err)
	}
	chars := map[rune]bool{}
	var buf sfnt.Buffer
	for r := rune(0x20); r <= 0xFFFF; r++ {
		if gi, err := f.GlyphIndex(&buf, r); err == nil && gi != 0 {
			chars[r] = true
		}
	}
	return Font{Name: name, Chars: chars, Scalable: true}, true, nil
}

// bfttfKey — ключ, которым на Switch зашифрованы BFTTF/BFOTF: каждое 4-байтовое слово XOR с ним.
var bfttfKey = [4]byte{0x49, 0x62, 0x18, 0x06}

// switchFont возвращает TTF/OTF: как есть или расшифрованный из BFTTF/BFOTF. nil — не векторный шрифт.
func switchFont(b []byte) []byte {
	if len(b) < 12 {
		return nil
	}
	if sfntMagic(b) {
		return b
	}
	var hdr [8]byte
	for i := range hdr {
		hdr[i] = b[i] ^ bfttfKey[i%4]
	}
	n := int(binary.BigEndian.Uint32(hdr[4:]))
	if binary.BigEndian.Uint32(hdr[:]) != 0x7F9A0218 || n <= 0 || 8+n > len(b) {
		return nil
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = b[8+i] ^ bfttfKey[(8+i)%4]
	}
	if !sfntMagic(out) {
		return nil
	}
	return out
}

func sfntMagic(b []byte) bool {
	m := string(b[:4])
	return m == "\x00\x01\x00\x00" || m == "OTTO" || m == "true"
}

// fontChars читает таблицы CMAP шрифта: какие символы в нём нарисованы.
func fontChars(b []byte) (map[rune]bool, error) {
	if len(b) < 0x20 {
		return nil, fmt.Errorf("BFFNT обрезан")
	}
	bo := byteOrder(b[4:6])
	hdr := int(bo.Uint16(b[6:]))
	wide := bo.Uint32(b[8:]) >= 0x04000000 // у Switch коды символов 32-битные, у 3DS — 16-битные
	if hdr+0x20 > len(b) || string(b[hdr:hdr+4]) != "FINF" {
		return nil, fmt.Errorf("BFFNT без FINF")
	}
	cmap := int(bo.Uint32(b[hdr+0x1C:]))
	chars := map[rune]bool{}
	for guard := 0; cmap > 0 && cmap+12 <= len(b) && guard < 10000; guard++ {
		var begin, end rune
		o := cmap
		if wide {
			begin, end = rune(bo.Uint32(b[o:])), rune(bo.Uint32(b[o+4:]))
			o += 8
		} else {
			begin, end = rune(bo.Uint16(b[o:])), rune(bo.Uint16(b[o+2:]))
			o += 4
		}
		method := bo.Uint16(b[o:])
		next := int(bo.Uint32(b[o+4:]))
		data := o + 8
		switch method {
		case 0: // подряд
			for c := begin; c <= end; c++ {
				chars[c] = true
			}
		case 1: // таблица индексов, 0xFFFF — нет символа
			for c := begin; c <= end; c++ {
				i := data + int(c-begin)*2
				if i+2 > len(b) {
					break
				}
				if bo.Uint16(b[i:]) != 0xFFFF {
					chars[c] = true
				}
			}
		case 2: // список пар «код — индекс»
			n := int(bo.Uint16(b[data:]))
			if wide {
				// u16 число, u16 выравнивание, затем пары (u32 код, u16 индекс, u16 выравнивание)
				for i := range n {
					e := data + 4 + i*8
					if e+8 > len(b) {
						break
					}
					chars[rune(bo.Uint32(b[e:]))] = true
				}
			} else {
				for i := range n {
					e := data + 2 + i*4
					if e+4 > len(b) {
						break
					}
					chars[rune(bo.Uint16(b[e:]))] = true
				}
			}
		}
		if next == 0 || next == cmap {
			break
		}
		cmap = next
	}
	return chars, nil
}

// Missing — символы из текста, которых нет в шрифте (без пробелов и служебных кодов).
func (f Font) Missing(text string) []rune {
	seen := map[rune]bool{}
	var out []rune
	for _, r := range codeRe.ReplaceAllString(text, "") {
		if r <= ' ' || seen[r] || f.Chars[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// PatchFonts пропускает каждый шрифт BFFNT файла (в том числе внутри SARC и Yaz0/zstd) через patch
// и собирает файл обратно с той же упаковкой. patch возвращает nil, если шрифт не менялся.
// Если не изменилось ничего, возвращает nil.
func PatchFonts(file string, data []byte, patch func(name string, font []byte) ([]byte, error)) ([]byte, error) {
	raw, wrap, err := codec.Unwrap(data)
	if err != nil {
		return nil, err
	}
	var out []byte
	switch {
	case bytes.HasPrefix(raw, []byte("FFNT")):
		if out, err = patch(file, raw); err != nil || out == nil {
			return nil, err
		}
	case bytes.HasPrefix(raw, []byte("SARC")):
		files, err := parseSARC(raw)
		if err != nil {
			return nil, err
		}
		changed := make([][]byte, len(files))
		any := false
		for i, f := range files {
			inner, w, err := codec.Unwrap(f.Data)
			if err != nil || !bytes.HasPrefix(inner, []byte("FFNT")) {
				continue
			}
			nb, err := patch(file+"/"+f.Name, inner)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			if nb == nil {
				continue
			}
			if changed[i], err = codec.Wrap(nb, w, f.Data); err != nil {
				return nil, err
			}
			any = true
		}
		if !any {
			return nil, nil
		}
		if out, err = rebuildSARC(raw, changed); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	return codec.Wrap(out, wrap, data)
}
