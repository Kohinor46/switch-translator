package gametext

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"unicode/utf16"

	"switch-translator/internal/codec"
	"switch-translator/internal/unity"
)

// Apply вписывает переводы в файл игры. tr — перевод по String.ID.
// Возвращает новый файл (с той же упаковкой Yaz0/zstd/SARC) и число вписанных строк;
// если вписывать нечего, возвращает nil, 0, nil.
func Apply(file string, data []byte, tr map[string]string) ([]byte, int, error) {
	raw, wrap, err := codec.Unwrap(data)
	if err != nil {
		return nil, 0, err
	}
	var out []byte
	var n int
	switch {
	case unity.IsSerialized(raw):
		out, n, err = applyUnity(file, raw, tr)
	case isRARC(raw):
		out, n, err = applyRARC(file, raw, tr)
	case IsLocres(raw):
		out, n, err = ApplyLocres(file, raw, nil, tr)
	case bytes.HasPrefix(raw, []byte("MsgStdBn")):
		out, n, err = applyMSBT(raw, path.Join(file, ""), tr)
	case bytes.HasPrefix(raw, []byte("SARC")):
		out, n, err = applySARC(raw, file, tr)
	default:
		return nil, 0, nil
	}
	if err != nil || n == 0 {
		return nil, 0, err
	}
	out, err = codec.Wrap(out, wrap, data)
	return out, n, err
}

func applySARC(data []byte, file string, tr map[string]string) ([]byte, int, error) {
	files, err := parseSARC(data)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	changed := make([][]byte, len(files))
	for i, f := range files {
		inner, wrap, err := codec.Unwrap(f.Data)
		if err != nil || !bytes.HasPrefix(inner, []byte("MsgStdBn")) {
			continue
		}
		nb, n, err := applyMSBT(inner, path.Join(file, f.Name), tr)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", f.Name, err)
		}
		if n == 0 {
			continue
		}
		if changed[i], err = codec.Wrap(nb, wrap, f.Data); err != nil {
			return nil, 0, err
		}
		total += n
	}
	if total == 0 {
		return nil, 0, nil
	}
	out, err := rebuildSARC(data, changed)
	return out, total, err
}

// rebuildSARC меняет содержимое файлов архива, оставляя имена, порядок и выравнивание.
func rebuildSARC(data []byte, changed [][]byte) ([]byte, error) {
	bo := byteOrder(data[6:8])
	hdrLen := int(bo.Uint16(data[4:]))
	dataOff := int(bo.Uint32(data[0xC:]))
	count := int(bo.Uint16(data[hdrLen+6:]))
	nodes := hdrLen + 0xC

	out := make([]byte, dataOff, len(data)+len(data)/4)
	copy(out, data[:dataOff])
	for i := range count {
		n := nodes + i*0x10
		start, end := dataOff+int(bo.Uint32(data[n+8:])), dataOff+int(bo.Uint32(data[n+12:]))
		body := data[start:end]
		if changed[i] != nil {
			body = changed[i]
		}
		// выравнивание как у оригинала: наибольшая степень двойки, на которую делилось начало файла
		al := 4
		for a := 0x2000; a >= 4; a >>= 1 {
			if start%a == 0 {
				al = a
				break
			}
		}
		for len(out)%al != 0 {
			out = append(out, 0)
		}
		ns := len(out) - dataOff
		out = append(out, body...)
		bo.PutUint32(out[n+8:], uint32(ns))
		bo.PutUint32(out[n+12:], uint32(ns+len(body)))
	}
	bo.PutUint32(out[8:], uint32(len(out)))
	return out, nil
}

// applyMSBT пересобирает секцию TXT2: переведённые строки кодируются заново, остальные остаются байт в байт.
func applyMSBT(data []byte, id string, tr map[string]string) ([]byte, int, error) {
	msgs, err := parseMSBT(data)
	if err != nil {
		return nil, 0, err
	}
	news := map[int]string{}
	for _, m := range msgs {
		if t, ok := tr[id+"#"+m.Label]; ok && t != "" && t != m.Text {
			news[m.Index] = t
		}
	}
	if len(news) == 0 {
		return nil, 0, nil
	}
	bo := byteOrder(data[8:10])
	enc := data[0xC]
	nsec := int(bo.Uint16(data[0xE:]))

	out := append([]byte(nil), data[:0x20]...)
	pos := 0x20
	for s := 0; s < nsec && pos+0x10 <= len(data); s++ {
		magic := sectionMagic(data[pos : pos+4])
		size := int(bo.Uint32(data[pos+4:]))
		body := pos + 0x10
		sec := data[body : body+size]
		if magic == "TXT2" {
			nb, err := rebuildTXT2(sec, enc, bo, news)
			if err != nil {
				return nil, 0, err
			}
			sec = nb
		}
		hdr := append([]byte(nil), data[pos:body]...)
		bo.PutUint32(hdr[4:], uint32(len(sec)))
		out = append(out, hdr...)
		out = append(out, sec...)
		for len(out)%16 != 0 {
			out = append(out, 0xAB)
		}
		pos = (body + size + 15) &^ 15
	}
	bo.PutUint32(out[0x12:], uint32(len(out)))
	return out, len(news), nil
}

func rebuildTXT2(sec []byte, enc byte, bo binary.ByteOrder, news map[int]string) ([]byte, error) {
	cnt := int(bo.Uint32(sec))
	head := 4 + cnt*4
	out := make([]byte, head)
	bo.PutUint32(out, uint32(cnt))
	for i := range cnt {
		start := int(bo.Uint32(sec[4+i*4:]))
		end := len(sec)
		if i+1 < cnt {
			end = int(bo.Uint32(sec[4+(i+1)*4:]))
		}
		body := sec[start:end]
		if t, ok := news[i]; ok {
			var err error
			if body, err = encodeLMS(t, enc, bo); err != nil {
				return nil, fmt.Errorf("строка %d: %w", i, err)
			}
		}
		bo.PutUint32(out[4+i*4:], uint32(len(out)))
		out = append(out, body...)
	}
	return out, nil
}

var tagRe = regexp.MustCompile(`\{\{(/?)(\d+):(\d+)(?::([0-9a-f]*))?\}\}`)

// encodeLMS — обратное к decodeLMS: текст с {{тегами}} → байты строки с нулём в конце.
func encodeLMS(s string, enc byte, bo binary.ByteOrder) ([]byte, error) {
	if len(codeRe.FindAllString(s, -1)) != len(tagRe.FindAllString(s, -1)) {
		return nil, fmt.Errorf("испорчен служебный код в «%s»", s)
	}
	if enc != 1 {
		return append([]byte(s), 0), nil
	}
	var out []byte
	put := func(v uint16) {
		var b [2]byte
		bo.PutUint16(b[:], v)
		out = append(out, b[:]...)
	}
	text := func(t string) {
		for _, u := range utf16.Encode([]rune(t)) {
			put(u)
		}
	}
	last := 0
	for _, m := range tagRe.FindAllStringSubmatchIndex(s, -1) {
		text(s[last:m[0]])
		last = m[1]
		g, _ := strconv.Atoi(s[m[4]:m[5]])
		t, _ := strconv.Atoi(s[m[6]:m[7]])
		if m[3] > m[2] { // закрывающий {{/g:t}}
			put(0x0F)
			put(uint16(g))
			put(uint16(t))
			continue
		}
		var params []byte
		if m[8] >= 0 {
			var err error
			if params, err = hex.DecodeString(s[m[8]:m[9]]); err != nil {
				return nil, fmt.Errorf("испорчен код %s", s[m[0]:m[1]])
			}
		}
		put(0x0E)
		put(uint16(g))
		put(uint16(t))
		put(uint16(len(params)))
		out = append(out, params...)
	}
	text(s[last:])
	put(0)
	return out, nil
}

// CodesMatch — одинаковы ли служебные коды в оригинале и переводе (порядок может меняться).
func CodesMatch(src, dst string) bool {
	a, b := codeRe.FindAllString(src, -1), codeRe.FindAllString(dst, -1)
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, c := range a {
		seen[c]++
	}
	for _, c := range b {
		seen[c]--
		if seen[c] < 0 {
			return false
		}
	}
	return true
}

// ArchiveFile — файл внутри архива SARC.
type ArchiveFile struct {
	Name string
	Data []byte // как лежит в архиве (может быть сжат Yaz0/zstd)
}

// ArchiveFiles — содержимое архива SARC (сам архив может быть сжат).
func ArchiveFiles(data []byte) ([]ArchiveFile, error) {
	raw, _, err := codec.Unwrap(data)
	if err != nil {
		return nil, err
	}
	files, err := parseSARC(raw)
	if err != nil {
		return nil, err
	}
	out := make([]ArchiveFile, len(files))
	for i, f := range files {
		out[i] = ArchiveFile{f.Name, f.Data}
	}
	return out, nil
}

// ReplaceInArchive подменяет один файл архива SARC и собирает архив с той же упаковкой.
func ReplaceInArchive(data []byte, name string, body []byte) ([]byte, error) {
	raw, wrap, err := codec.Unwrap(data)
	if err != nil {
		return nil, err
	}
	files, err := parseSARC(raw)
	if err != nil {
		return nil, err
	}
	changed := make([][]byte, len(files))
	found := false
	for i, f := range files {
		if f.Name == name {
			changed[i], found = body, true
		}
	}
	if !found {
		return nil, fmt.Errorf("в архиве нет файла %s", name)
	}
	out, err := rebuildSARC(raw, changed)
	if err != nil {
		return nil, err
	}
	return codec.Wrap(out, wrap, data)
}
