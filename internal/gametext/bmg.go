package gametext

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"switch-translator/internal/codec"
)

// Тексты игр GameCube/Wii и их портов (Super Mario Galaxy 1/2): архивы RARC с сообщениями BMG.
// В портах для Switch и архив, и BMG записаны в обратном порядке байт: магия «CRAR», «GSEM1gmb».

type rarcFile struct {
	Name  string
	Data  []byte
	entry int // смещение записи файла в архиве
}

type rarc struct {
	raw              []byte
	bo               binary.ByteOrder
	dataStart        int
	files            []rarcFile
	entries, entryCt int
}

func isRARC(b []byte) bool {
	return len(b) >= 0x40 && (string(b[:4]) == "RARC" || string(b[:4]) == "CRAR")
}

func parseRARC(b []byte) (*rarc, error) {
	if !isRARC(b) {
		return nil, fmt.Errorf("не RARC")
	}
	a := &rarc{raw: b, bo: binary.BigEndian}
	if string(b[:4]) == "CRAR" {
		a.bo = binary.LittleEndian
	}
	bo := a.bo
	a.dataStart = 0x20 + int(bo.Uint32(b[0x0C:]))
	info := 0x20
	a.entryCt = int(bo.Uint32(b[info+0x08:]))
	a.entries = info + int(bo.Uint32(b[info+0x0C:]))
	strs := info + int(bo.Uint32(b[info+0x14:]))
	if a.entries+a.entryCt*0x14 > len(b) || strs > len(b) || a.dataStart > len(b) {
		return nil, fmt.Errorf("RARC: таблицы за концом файла")
	}
	for i := range a.entryCt {
		e := a.entries + i*0x14
		tn := bo.Uint32(b[e+4:])
		if tn>>24&0x01 == 0 { // папка
			continue
		}
		nameOff := strs + int(tn&0xFFFFFF)
		name := ""
		if nameOff < len(b) {
			if k := bytes.IndexByte(b[nameOff:], 0); k >= 0 {
				name = string(b[nameOff : nameOff+k])
			}
		}
		off := a.dataStart + int(bo.Uint32(b[e+8:]))
		size := int(bo.Uint32(b[e+12:]))
		if off+size > len(b) || off < a.dataStart {
			return nil, fmt.Errorf("RARC: %s за концом файла", name)
		}
		a.files = append(a.files, rarcFile{Name: name, Data: b[off : off+size], entry: e})
	}
	return a, nil
}

// replace — архив, в котором файл i заменён на data; данные файлов после него сдвигаются (выравнивание 32).
func (a *rarc) replace(i int, data []byte) []byte {
	bo := a.bo
	f := a.files[i]
	oldOff := int(bo.Uint32(a.raw[f.entry+8:]))
	oldEnd := oldOff + align32(len(f.Data))
	delta := align32(len(data)) - align32(len(f.Data))
	start := a.dataStart + oldOff
	out := make([]byte, 0, len(a.raw)+delta)
	out = append(out, a.raw[:start]...)
	out = append(out, data...)
	out = append(out, make([]byte, align32(len(data))-len(data))...)
	if a.dataStart+oldEnd < len(a.raw) {
		out = append(out, a.raw[a.dataStart+oldEnd:]...)
	}
	bo.PutUint32(out[f.entry+12:], uint32(len(data)))
	for _, g := range a.files {
		if off := int(bo.Uint32(out[g.entry+8:])); off > oldOff {
			bo.PutUint32(out[g.entry+8:], uint32(off+delta))
		}
	}
	bo.PutUint32(out[4:], uint32(len(out)))
	dataLen := bo.Uint32(out[0x10:])
	bo.PutUint32(out[0x10:], uint32(int(dataLen)+delta))
	if mram := bo.Uint32(out[0x14:]); mram == dataLen {
		bo.PutUint32(out[0x14:], uint32(int(mram)+delta))
	}
	return out
}

func align32(n int) int { return (n + 31) &^ 31 }

// bmg — сообщения BMG: тексты по порядку записей INF1.
type bmg struct {
	raw       []byte
	bo        binary.ByteOrder
	enc       byte // 1 — CP1252, 2 — UTF-16, 3 — Shift-JIS, 4 — UTF-8
	inf, dat  int  // начала блоков
	count     int
	entrySize int
	offsets   []int // смещение строки каждой записи в DAT1
}

func isBMG(b []byte) bool {
	return len(b) >= 0x20 && (string(b[:8]) == "MESGbmg1" || string(b[:8]) == "GSEM1gmb")
}

func parseBMG(b []byte) (*bmg, error) {
	if !isBMG(b) {
		return nil, fmt.Errorf("не BMG")
	}
	m := &bmg{raw: b, bo: binary.BigEndian, enc: b[0x10], inf: -1, dat: -1}
	infMagic, datMagic := "INF1", "DAT1"
	if string(b[:4]) == "GSEM" {
		m.bo = binary.LittleEndian
		infMagic, datMagic = "1FNI", "1TAD"
	}
	blocks := int(m.bo.Uint32(b[0x0C:]))
	p := 0x20
	for range blocks {
		if p+8 > len(b) {
			break
		}
		size := int(m.bo.Uint32(b[p+4:]))
		switch string(b[p : p+4]) {
		case infMagic:
			m.inf = p
		case datMagic:
			m.dat = p
		}
		if size <= 0 {
			break
		}
		p += size
	}
	if m.inf < 0 || m.dat < 0 {
		return nil, fmt.Errorf("BMG: нет INF1/DAT1")
	}
	m.count = int(m.bo.Uint16(b[m.inf+8:]))
	m.entrySize = int(m.bo.Uint16(b[m.inf+10:]))
	if m.entrySize < 4 || m.inf+16+m.count*m.entrySize > len(b) {
		return nil, fmt.Errorf("BMG: записи INF1 за концом блока")
	}
	for i := range m.count {
		m.offsets = append(m.offsets, int(m.bo.Uint32(b[m.inf+16+i*m.entrySize:])))
	}
	return m, nil
}

func (m *bmg) datEnd() int { return m.dat + int(m.bo.Uint32(m.raw[m.dat+4:])) }

// text — сообщение i: служебные коды (цвет, пауза, иконки) — {{bmg:…}}.
func (m *bmg) text(i int) string {
	b := m.raw[m.dat+8+m.offsets[i] : min(len(m.raw), m.datEnd())]
	var sb strings.Builder
	switch m.enc {
	case 2:
		var run []uint16
		flush := func() { sb.WriteString(string(utf16.Decode(run))); run = run[:0] }
		for p := 0; p+2 <= len(b); {
			c := m.bo.Uint16(b[p:])
			if c == 0 {
				break
			}
			if c == 0x1A && p+2 < len(b) {
				n := int(b[p+2]) // длина всей последовательности, вместе с 1A
				end := min(p+max(n, 3), len(b))
				flush()
				fmt.Fprintf(&sb, "{{bmg:%s}}", hex.EncodeToString(b[p+2:end]))
				p = end
				continue
			}
			run = append(run, c)
			p += 2
		}
		flush()
	default:
		for p := 0; p < len(b) && b[p] != 0; {
			if b[p] == 0x1A && p+1 < len(b) {
				n := int(b[p+1])
				end := min(p+max(n, 2), len(b))
				fmt.Fprintf(&sb, "{{bmg:%s}}", hex.EncodeToString(b[p+1:end]))
				p = end
				continue
			}
			sb.WriteByte(b[p])
			p++
		}
	}
	return sb.String()
}

var bmgCodeRe = regexp.MustCompile(`\{\{bmg:([0-9a-f]*)\}\}`)

func (m *bmg) encode(s string) ([]byte, error) {
	if len(codeRe.FindAllString(s, -1)) != len(bmgCodeRe.FindAllString(s, -1)) {
		return nil, fmt.Errorf("испорчен служебный код в «%s»", s)
	}
	var out []byte
	text := func(t string) error {
		switch m.enc {
		case 2:
			for _, u := range utf16.Encode([]rune(t)) {
				out = appendU16(m.bo, out, u)
			}
		case 4:
			out = append(out, t...)
		default:
			for _, r := range t {
				if r > 0xFF {
					return fmt.Errorf("в этой кодировке BMG нет буквы «%c»", r)
				}
				out = append(out, byte(r))
			}
		}
		return nil
	}
	last := 0
	for _, mt := range bmgCodeRe.FindAllStringSubmatchIndex(s, -1) {
		if err := text(s[last:mt[0]]); err != nil {
			return nil, err
		}
		last = mt[1]
		raw, err := hex.DecodeString(s[mt[2]:mt[3]])
		if err != nil {
			return nil, fmt.Errorf("испорчен код %s", s[mt[0]:mt[1]])
		}
		if m.enc == 2 {
			out = appendU16(m.bo, out, 0x1A)
		} else {
			out = append(out, 0x1A)
		}
		out = append(out, raw...)
	}
	if err := text(s[last:]); err != nil {
		return nil, err
	}
	if m.enc == 2 {
		return append(out, 0, 0), nil
	}
	return append(out, 0), nil
}

// rebuild — BMG с новыми текстами (номер сообщения → текст). Пул строк DAT1 собирается заново.
func (m *bmg) rebuild(news map[int]string) ([]byte, error) {
	b := m.raw
	var pool []byte
	if m.enc == 2 {
		pool = []byte{0, 0}
	} else {
		pool = []byte{0}
	}
	shared := map[int]int{} // старое смещение → новое (у нескольких записей бывает одна строка)
	newOff := make([]int, m.count)
	for i, off := range m.offsets {
		if t, ok := news[i]; ok {
			enc, err := m.encode(t)
			if err != nil {
				return nil, err
			}
			newOff[i] = len(pool)
			pool = append(pool, enc...)
			continue
		}
		if off == 0 {
			continue
		}
		if o, ok := shared[off]; ok {
			newOff[i] = o
			continue
		}
		enc, err := m.encode(m.text(i))
		if err != nil {
			return nil, err
		}
		shared[off] = len(pool)
		newOff[i] = len(pool)
		pool = append(pool, enc...)
	}
	datLen := align32(8 + len(pool))
	out := make([]byte, 0, len(b)+len(pool))
	out = append(out, b[:m.dat+8]...)
	out = append(out, pool...)
	out = append(out, make([]byte, datLen-8-len(pool))...)
	out = append(out, b[m.datEnd():]...)
	m.bo.PutUint32(out[m.dat+4:], uint32(datLen))
	for i, o := range newOff {
		if m.inf < m.dat {
			m.bo.PutUint32(out[m.inf+16+i*m.entrySize:], uint32(o))
		}
	}
	if m.inf > m.dat { // INF1 после DAT1 — сдвинулся
		shift := len(out) - len(b)
		for i, o := range newOff {
			m.bo.PutUint32(out[m.inf+shift+16+i*m.entrySize:], uint32(o))
		}
	}
	switch size := int(m.bo.Uint32(b[8:])); {
	case size == len(b):
		m.bo.PutUint32(out[8:], uint32(len(out)))
	case size == len(b)/32:
		m.bo.PutUint32(out[8:], uint32(len(out)/32))
	}
	return out, nil
}

// fromRARC — строки всех BMG архива RARC.
func fromRARC(file string, data []byte) ([]String, error) {
	a, err := parseRARC(data)
	if err != nil {
		return nil, err
	}
	loc := Locale(file)
	var out []String
	for _, f := range a.files {
		inner, _, err := codec.Unwrap(f.Data)
		if err != nil {
			continue
		}
		if bytes.HasPrefix(inner, []byte("MsgStdBn")) { // Super Mario Galaxy 2: MSBT в RARC
			strs, err := fromMSBT(file, f.Name, inner)
			if err != nil {
				return out, fmt.Errorf("%s/%s: %w", file, f.Name, err)
			}
			out = append(out, strs...)
			continue
		}
		if !isBMG(inner) {
			continue
		}
		m, err := parseBMG(inner)
		if err != nil {
			return out, fmt.Errorf("%s/%s: %w", file, f.Name, err)
		}
		for i := range m.count {
			if m.offsets[i] == 0 {
				continue
			}
			text := m.text(i)
			if strings.TrimSpace(StripCodes(text)) == "" {
				continue
			}
			out = append(out, String{
				ID:     path.Join(file, f.Name) + "#" + strconv.Itoa(i),
				File:   file,
				Inner:  f.Name,
				Label:  strconv.Itoa(i),
				Index:  i,
				Locale: loc,
				Source: text,
				Skip:   !HasLetters(text),
			})
		}
	}
	return out, nil
}

func applyRARC(file string, data []byte, tr map[string]string) ([]byte, int, error) {
	a, err := parseRARC(data)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	for i := range a.files {
		f := a.files[i]
		inner, wrap, err := codec.Unwrap(f.Data)
		if err != nil {
			continue
		}
		var nb []byte
		var n int
		if bytes.HasPrefix(inner, []byte("MsgStdBn")) {
			if nb, n, err = applyMSBT(inner, path.Join(file, f.Name), tr); err != nil {
				return nil, 0, fmt.Errorf("%s: %w", f.Name, err)
			}
		} else if isBMG(inner) {
			if nb, n, err = applyBMG(inner, path.Join(file, f.Name), tr); err != nil {
				return nil, 0, fmt.Errorf("%s: %w", f.Name, err)
			}
		}
		if n == 0 {
			continue
		}
		if nb, err = codec.Wrap(nb, wrap, f.Data); err != nil {
			return nil, 0, err
		}
		data = a.replace(i, nb)
		if a, err = parseRARC(data); err != nil {
			return nil, 0, err
		}
		total += n
	}
	if total == 0 {
		return nil, 0, nil
	}
	return data, total, nil
}

// applyBMG вписывает переводы в BMG (id — «архив/файл.bmg»).
func applyBMG(inner []byte, id string, tr map[string]string) ([]byte, int, error) {
	m, err := parseBMG(inner)
	if err != nil {
		return nil, 0, err
	}
	prefix := id + "#"
	news := map[int]string{}
	for id, t := range tr {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			if k, err := strconv.Atoi(rest); err == nil && k >= 0 && k < m.count {
				news[k] = t
			}
		}
	}
	if len(news) == 0 {
		return nil, 0, nil
	}
	nb, err := m.rebuild(news)
	return nb, len(news), err
}

func appendU16(bo binary.ByteOrder, b []byte, v uint16) []byte {
	b = append(b, 0, 0)
	bo.PutUint16(b[len(b)-2:], v)
	return b
}
