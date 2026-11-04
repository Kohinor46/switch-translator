// Package unreal читает архивы .pak Unreal Engine 4 и таблицы локализации .locres,
// а также собирает маленький .pak-заплатку (*_P.pak) с изменёнными файлами: движок
// подключает такие архивы поверх основного, поэтому огромный оригинал переписывать не нужно.
package unreal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"

	"switch-translator/internal/oodle"
)

const pakMagic = 0x5A6F12E1

// ErrEncrypted — индекс архива зашифрован (нужен ключ AES игры).
var ErrEncrypted = errors.New("индекс .pak зашифрован — без ключа игры его не прочитать")

// Entry — файл в архиве.
type Entry struct {
	Name          string // путь с точкой монтирования: ../../../Game/Content/…
	Offset        int64  // где начинается запись (заголовок FPakEntry, потом данные)
	Size, RawSize int64  // сжатый и исходный размер
	Compression   uint32 // 0 — без сжатия
	Blocks        [][2]int64
	Encrypted     bool
	BlockSize     uint32
	headerSize    int64
}

// Pak — оглавление архива.
type Pak struct {
	Version     int32
	MountPoint  string
	Entries     []Entry
	Compression []string // имена методов сжатия (версия 8+): Zlib, Oodle…
	Encrypted   bool
	r           io.ReaderAt
	size        int64
}

// OpenPak читает оглавление архива размера size.
func OpenPak(r io.ReaderAt, size int64) (*Pak, error) {
	tailLen := min(size, 512)
	tail := make([]byte, tailLen)
	if _, err := r.ReadAt(tail, size-tailLen); err != nil && err != io.EOF {
		return nil, err
	}
	le := binary.LittleEndian
	m := -1
	for i := len(tail) - 44; i >= 0; i-- {
		if le.Uint32(tail[i:]) == pakMagic {
			v := int32(le.Uint32(tail[i+4:]))
			if v >= 1 && v <= 12 {
				m = i
				break
			}
		}
	}
	if m < 0 {
		return nil, fmt.Errorf(".pak: нет подписи в конце файла")
	}
	p := &Pak{r: r, size: size, Version: int32(le.Uint32(tail[m+4:]))}
	indexOff := int64(le.Uint64(tail[m+8:]))
	indexSize := int64(le.Uint64(tail[m+16:]))
	if p.Version >= 4 && m >= 1 {
		p.Encrypted = tail[m-1] != 0
	}
	if p.Version >= 8 {
		names := tail[m+44:]
		if p.Version == 9 {
			names = names[1:]
		}
		for i := 0; i+32 <= len(names); i += 32 {
			n := string(bytes.TrimRight(names[i:i+32], "\x00"))
			if n == "" {
				continue
			}
			p.Compression = append(p.Compression, n)
		}
	}
	if p.Encrypted {
		return p, ErrEncrypted
	}
	if indexOff < 0 || indexSize <= 0 || indexOff+indexSize > size || indexSize > 1<<30 {
		return nil, fmt.Errorf(".pak: оглавление за концом файла")
	}
	idx := make([]byte, indexSize)
	if _, err := r.ReadAt(idx, indexOff); err != nil && err != io.EOF {
		return nil, err
	}
	rd := &reader{b: idx}
	p.MountPoint = rd.fstring()
	n := int(rd.i32())
	if n < 0 || n > 1<<22 {
		return nil, fmt.Errorf(".pak: %d файлов", n)
	}
	if p.Version >= 10 {
		return p, p.readIndexV10(rd, n, size)
	}
	for range n {
		name := rd.fstring()
		e := p.readEntry(rd)
		if rd.err != nil {
			return nil, rd.err
		}
		e.Name = p.MountPoint + name
		p.Entries = append(p.Entries, e)
	}
	return p, rd.err
}

// readEntry — FPakEntry в оглавлении (версии до 10) или перед данными файла.
func (p *Pak) readEntry(rd *reader) Entry {
	start := rd.p
	var e Entry
	e.Offset = rd.i64()
	e.Size = rd.i64()
	e.RawSize = rd.i64()
	e.Compression = uint32(rd.i32())
	if p.Version < 2 {
		rd.i64() // время
	}
	rd.bytes(20) // SHA-1
	if p.Version >= 3 {
		if e.Compression != 0 {
			nb := int(rd.i32())
			for range nb {
				e.Blocks = append(e.Blocks, [2]int64{rd.i64(), rd.i64()})
			}
		}
		e.Encrypted = rd.u8() != 0
		e.BlockSize = uint32(rd.i32())
	}
	e.headerSize = int64(rd.p - start)
	return e
}

// readIndexV10 — оглавление версий 10–11: записи закодированы, имена — в индексе папок.
func (p *Pak) readIndexV10(rd *reader, n int, size int64) error {
	rd.u64() // seed хэшей путей
	if rd.i32() != 0 {
		rd.i64()
		rd.i64()
		rd.bytes(20)
	}
	hasDir := rd.i32() != 0
	var dirOff, dirSize int64
	if hasDir {
		dirOff, dirSize = rd.i64(), rd.i64()
		rd.bytes(20)
	}
	encSize := int(rd.i32())
	enc := rd.bytes(encSize)
	if rd.err != nil {
		return rd.err
	}
	if !hasDir {
		return fmt.Errorf(".pak версии %d без индекса папок", p.Version)
	}
	if dirOff < 0 || dirSize <= 0 || dirOff+dirSize > size || dirSize > 1<<30 {
		return fmt.Errorf(".pak: индекс папок за концом файла")
	}
	dir, err := p.readAt(dirOff, dirSize, "индекс папок")
	if err != nil {
		return err
	}
	dr := &reader{b: dir}
	dirs := int(dr.i32())
	for range dirs {
		dname := dr.fstring()
		files := int(dr.i32())
		for range files {
			fname := dr.fstring()
			off := int(dr.i32())
			if dr.err != nil {
				return dr.err
			}
			if off < 0 || off >= len(enc) {
				continue // запись вне закодированного списка (для простоты не читаем)
			}
			e, ok := p.decodeEntry(enc[off:])
			if !ok {
				continue
			}
			e.Name = p.MountPoint + dname + fname
			p.Entries = append(p.Entries, e)
		}
	}
	return dr.err
}

// decodeEntry — сжатая запись FPakEntry (версия 10+).
func (p *Pak) decodeEntry(b []byte) (Entry, bool) {
	if len(b) < 4 {
		return Entry{}, false
	}
	le := binary.LittleEndian
	bits := le.Uint32(b)
	pos := 4
	var e Entry
	e.Compression = (bits >> 23) & 0x3F
	e.Encrypted = bits&(1<<22) != 0
	blocks := int((bits >> 6) & 0xFFFF)
	e.BlockSize = (bits & 0x3F) << 11
	if bits&0x3F == 0x3F {
		if pos+4 > len(b) {
			return e, false
		}
		e.BlockSize = le.Uint32(b[pos:])
		pos += 4
	}
	read := func(is32 bool) int64 {
		if is32 {
			if pos+4 > len(b) {
				return 0
			}
			v := int64(le.Uint32(b[pos:]))
			pos += 4
			return v
		}
		if pos+8 > len(b) {
			return 0
		}
		v := int64(le.Uint64(b[pos:]))
		pos += 8
		return v
	}
	e.Offset = read(bits&(1<<31) != 0)
	e.RawSize = read(bits&(1<<30) != 0)
	if e.Compression != 0 {
		e.Size = read(bits&(1<<29) != 0)
	} else {
		e.Size = e.RawSize
	}
	// заголовок перед данными: 8+8+8+4+20, у сжатых ещё блоки + 4, и 1+4
	e.headerSize = 53
	if e.Compression != 0 {
		e.headerSize += 4 + int64(blocks)*16
	}
	if e.Compression != 0 && blocks > 0 {
		start := e.headerSize
		if blocks == 1 && !e.Encrypted {
			e.Blocks = [][2]int64{{start, start + e.Size}}
		} else {
			for range blocks {
				sz := read(true)
				e.Blocks = append(e.Blocks, [2]int64{start, start + sz})
				if e.Encrypted {
					sz = (sz + 15) &^ 15
				}
				start += sz
			}
		}
	}
	return e, true
}

// Find — запись по концу пути (без учёта регистра).
func (p *Pak) Find(suffix string) (Entry, bool) {
	for _, e := range p.Entries {
		if strings.HasSuffix(strings.ToLower(e.Name), strings.ToLower(suffix)) {
			return e, true
		}
	}
	return Entry{}, false
}

// Read — содержимое файла архива (сжатое zlib или Oodle Kraken распаковывается; шифрование — нет).
func (p *Pak) Read(e Entry) ([]byte, error) {
	if e.Encrypted {
		return nil, fmt.Errorf("%s зашифрован", e.Name)
	}
	if e.Offset < 0 || e.Size < 0 || e.RawSize < 0 {
		return nil, fmt.Errorf("%s: испорченная запись оглавления", e.Name)
	}
	if e.Compression == 0 {
		return p.readAt(e.Offset+e.headerSize, e.RawSize, e.Name)
	}
	method := ""
	if int(e.Compression)-1 < len(p.Compression) && p.Version >= 8 {
		method = p.Compression[e.Compression-1]
	} else if e.Compression == 1 {
		method = "Zlib"
	}
	isOodle := strings.EqualFold(method, "oodle")
	if !strings.EqualFold(method, "zlib") && !isOodle {
		return nil, fmt.Errorf("%s: сжатие %q программа не распаковывает", e.Name, method)
	}
	blocks := e.Blocks
	if len(blocks) == 0 {
		if p.Version >= 3 {
			return nil, fmt.Errorf("%s: сжатая запись без таблицы блоков", e.Name)
		}
		// до версии 3 таблицы блоков нет: сжатые данные лежат одним потоком сразу за заголовком
		blocks = [][2]int64{{e.Offset + e.headerSize, e.Offset + e.headerSize + e.Size}}
	}
	if len(blocks) > 1 && e.BlockSize == 0 {
		return nil, fmt.Errorf("%s: несколько блоков при нулевом размере блока", e.Name)
	}
	out := make([]byte, 0, e.RawSize)
	for i, bl := range blocks {
		start, end := bl[0], bl[1]
		if p.Version >= 5 { // с версии 5 смещения блоков — от начала записи
			start, end = e.Offset+start, e.Offset+end
		}
		if end < start {
			return nil, fmt.Errorf("%s: блок %d испорчен", e.Name, i)
		}
		buf, err := p.readAt(start, end-start, e.Name)
		if err != nil {
			return nil, err
		}
		var dec []byte
		if isOodle {
			// блоки по BlockSize байт исходных данных, последний — остаток;
			// у единственного блока размер — весь файл (так считает и движок)
			raw := e.RawSize
			if len(blocks) > 1 {
				raw = min(e.RawSize-int64(i)*int64(e.BlockSize), int64(e.BlockSize))
			}
			if raw <= 0 {
				return nil, fmt.Errorf("%s: блоков больше, чем данных", e.Name)
			}
			dec, err = oodle.Decompress(buf, int(raw))
		} else {
			dec, err = inflate(buf)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}
		out = append(out, dec...)
	}
	return out, nil
}

// readAt — n байт архива с позиции off. Чтение за концом файла или не до конца — ошибка,
// а не буфер с нулями в хвосте.
func (p *Pak) readAt(off, n int64, what string) ([]byte, error) {
	if off < 0 || n < 0 || off+n > p.size {
		return nil, fmt.Errorf("%s: данные за пределами архива", what)
	}
	b := make([]byte, n)
	k, err := p.r.ReadAt(b, off)
	if k < len(b) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return b, nil
}

type reader struct {
	b   []byte
	p   int
	err error
}

func (r *reader) need(n int) bool {
	if r.err == nil && (n < 0 || r.p+n > len(r.b)) {
		r.err = fmt.Errorf(".pak: оглавление обрывается")
	}
	return r.err == nil
}
func (r *reader) u8() byte {
	if !r.need(1) {
		return 0
	}
	r.p++
	return r.b[r.p-1]
}
func (r *reader) i32() int32 {
	if !r.need(4) {
		return 0
	}
	r.p += 4
	return int32(binary.LittleEndian.Uint32(r.b[r.p-4:]))
}
func (r *reader) u32() uint32 { return uint32(r.i32()) }
func (r *reader) i64() int64 {
	if !r.need(8) {
		return 0
	}
	r.p += 8
	return int64(binary.LittleEndian.Uint64(r.b[r.p-8:]))
}
func (r *reader) u64() uint64 { return uint64(r.i64()) }
func (r *reader) bytes(n int) []byte {
	if !r.need(n) {
		return nil
	}
	r.p += n
	return r.b[r.p-n : r.p]
}

// fstring — строка UE: длина int32 вместе с нулём; отрицательная — UTF-16.
func (r *reader) fstring() string {
	n := int(r.i32())
	switch {
	case n == 0:
		return ""
	case n > 0:
		b := r.bytes(n)
		if b == nil {
			return ""
		}
		return string(bytes.TrimRight(b, "\x00"))
	default:
		b := r.bytes(-n * 2)
		if b == nil {
			return ""
		}
		u := make([]uint16, -n)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
		for len(u) > 0 && u[len(u)-1] == 0 {
			u = u[:len(u)-1]
		}
		return string(utf16.Decode(u))
	}
}
