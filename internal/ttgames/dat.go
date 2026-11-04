// Package ttgames читает архивы .DAT игр Traveller's Tales (LEGO): оглавление «.CC40TAD»
// с деревом имён и хэшами путей FNV-1a, файлы целиком или кусками LZ2K / DFLT.
package ttgames

import (
	"bytes"
	"compress/flate"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// File — файл архива.
type File struct {
	Name        string // путь в нижнем регистре через «\»
	Offset      int64
	ZSize, Size int64
	Packed      bool
	index       int // номер в таблице файлов
	entry       int // где запись в оглавлении
}

// DAT — оглавление архива.
type DAT struct {
	Version    int32 // -12 и т.п.
	FormatVer  uint32
	InfoOffset int64
	InfoSize   int64
	Files      []File
	CRCBits    int
	info       []byte
}

// Open читает оглавление архива.
func Open(r io.ReaderAt, size int64) (*DAT, error) {
	head := make([]byte, 8)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	off := int64(le.Uint32(head))
	if off&0x80000000 != 0 {
		off = int64(le.Uint32(head)^0xFFFFFFFF)<<8 + 0x100
	}
	isz := int64(le.Uint32(head[4:]))
	if off <= 0 || off+isz > size || isz < 32 || isz > 256<<20 {
		return nil, fmt.Errorf("DAT: оглавление за концом файла")
	}
	info := make([]byte, isz)
	if _, err := r.ReadAt(info, off); err != nil && err != io.EOF {
		return nil, err
	}
	if !bytes.Equal(info[4:12], []byte(".CC40TAD")) {
		return nil, fmt.Errorf("DAT: старый формат оглавления программа пока не читает")
	}
	d := &DAT{InfoOffset: off, InfoSize: isz, info: info}
	return d, d.parse()
}

func (d *DAT) parse() error {
	b := d.info
	be := binary.BigEndian
	p := 12
	u32 := func() uint32 {
		if p+4 > len(b) {
			p = len(b) + 1
			return 0
		}
		p += 4
		return be.Uint32(b[p-4:])
	}
	d.Version = int32(u32())
	d.FormatVer = u32()
	files := int(u32())
	names := int(u32())
	namesSize := int(u32())
	namesOff := p
	p += namesSize
	u32() // ?
	if p > len(b) || files > 1<<22 || names > 1<<22 {
		return fmt.Errorf("DAT: испорчено оглавление")
	}
	// дерево имён: у папки — свой номер записи, у файла — FILE_ID != 0
	paths := make([]string, names+1)
	var fileNames []string
	str := func(off int) string {
		off += namesOff
		if off < 0 || off >= len(b) {
			return ""
		}
		end := bytes.IndexByte(b[off:], 0)
		if end < 0 {
			return ""
		}
		return string(b[off : off+end])
	}
	for i := range names {
		nameOff := u32()
		folder := int(be.Uint16(b[p:]))
		p += 2
		if d.FormatVer >= 2 {
			p += 2
		}
		p += 2 // SOME_ID
		fileID := int(be.Uint16(b[p:]))
		p += 2
		if p > len(b) {
			return fmt.Errorf("DAT: имена обрываются")
		}
		if nameOff == 0xFFFFFFFF {
			continue
		}
		name := str(int(nameOff))
		full := name
		if folder < len(paths) && paths[folder] != "" {
			full = paths[folder] + "\\" + name
		}
		if fileID != 0 || i == names-1 {
			fileNames = append(fileNames, full)
		} else {
			paths[i] = full
		}
	}
	d.Version = int32(u32())
	if n := int(u32()); n != files {
		return fmt.Errorf("DAT: файлов %d и %d", files, n)
	}
	d.Files = make([]File, files)
	for i := range files {
		var f File
		f.entry = p
		if d.Version <= -11 {
			f.Offset = int64(be.Uint64(b[p:]))
			p += 8
		} else {
			f.Offset = int64(u32())
		}
		f.ZSize = int64(u32())
		size := u32()
		if d.Version <= -10 {
			f.Packed = size&0x80000000 != 0
			f.Size = int64(size & 0x7FFFFFFF)
		} else {
			f.Size = int64(size)
			packed := b[p]
			off2 := b[p+3]
			p += 4
			f.Packed = packed&2 != 0
			f.Offset = f.Offset<<8 | int64(off2)
		}
		f.index = i
		d.Files[i] = f
		if p > len(b) {
			return fmt.Errorf("DAT: таблица файлов обрывается")
		}
	}
	// хэши путей: по ним имя находит свою запись. Ширина (32 или 64 бита) не записана —
	// пробуются обе, берётся та, при которой совпало больше имён.
	norm := make([]string, len(fileNames))
	for i, n := range fileNames {
		norm[i] = strings.TrimPrefix(strings.ToUpper(strings.ReplaceAll(n, "/", "\\")), "\\")
	}
	best, bestHits := 0, -1
	var bestMap map[uint64]int
	for _, bits := range []int{64, 32} {
		if p+files*bits/8 > len(b) {
			continue
		}
		byCRC := make(map[uint64]int, files)
		for i := range files {
			if bits == 64 {
				byCRC[be.Uint64(b[p+i*8:])] = i
			} else {
				byCRC[uint64(be.Uint32(b[p+i*4:]))] = i
			}
		}
		d.CRCBits = bits
		hits := 0
		for _, n := range norm {
			if _, ok := byCRC[d.hash(n)]; ok {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits, bestMap = bits, hits, byCRC
		}
	}
	d.CRCBits = best
	for _, n := range norm {
		if i, ok := bestMap[d.hash(n)]; ok {
			d.Files[i].Name = strings.ToLower(n)
		}
	}
	return nil
}

func (d *DAT) hash(name string) uint64 {
	if d.CRCBits == 64 {
		h := uint64(0xcbf29ce484222325)
		for i := 0; i < len(name); i++ {
			h ^= uint64(name[i])
			h *= 1099511628211
		}
		return h
	}
	h := uint32(0x811c9dc5)
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 0x199933
	}
	return uint64(h)
}

// Named — файлы с именами по алфавиту.
func (d *DAT) Named() []File {
	var out []File
	for _, f := range d.Files {
		if f.Name != "" {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Read — содержимое файла: куски ZIPX (RC4, ключ — 4 байта размера куска; внутри deflate или как есть)
// и DFLT (deflate). LZ2K и RFPK (текстуры) программа не распаковывает.
func (d *DAT) Read(r io.ReaderAt, f File) ([]byte, error) {
	raw := make([]byte, f.ZSize)
	if _, err := r.ReadAt(raw, f.Offset); err != nil && err != io.EOF {
		return nil, err
	}
	if !f.Packed {
		return raw, nil
	}
	le := binary.LittleEndian
	var out []byte
	for p := 0; p+12 <= len(raw); {
		sign := string(raw[p : p+4])
		a, b := int(le.Uint32(raw[p+4:])), int(le.Uint32(raw[p+8:]))
		p += 12
		zsize, size := a, b
		if sign == "LZ2K" {
			zsize, size = b, a
		}
		if p+zsize > len(raw) {
			return nil, fmt.Errorf("%s: кусок за концом файла", f.Name)
		}
		chunk := raw[p : p+zsize]
		p += zsize
		switch sign {
		case "ZIPX":
			c, _ := rc4.NewCipher(raw[p-zsize-8 : p-zsize-4])
			dec := make([]byte, zsize)
			c.XORKeyStream(dec, chunk)
			if zsize == size {
				out = append(out, dec...)
				continue
			}
			u, err := inflateRaw(dec)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out = append(out, u...)
		case "DFLT":
			if zsize == size {
				out = append(out, chunk...)
				continue
			}
			u, err := inflateRaw(chunk)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out = append(out, u...)
		default:
			return nil, fmt.Errorf("%s: сжатие %q программа не распаковывает", f.Name, sign)
		}
	}
	return out, nil
}

func inflateRaw(b []byte) ([]byte, error) {
	return io.ReadAll(flate.NewReader(bytes.NewReader(b)))
}

// Rewrite пишет в w архив, где файлы replace (номер в Files → содержимое) заменены: новые данные
// дописываются после старых (несжатыми), оглавление — за ними, со смещениями на новые данные.
// Остальные файлы и порядок записей не меняются.
func (d *DAT) Rewrite(r io.ReaderAt, w io.Writer, replace map[int][]byte) error {
	if d.Version > -10 {
		return fmt.Errorf("DAT: запись в архив версии %d программа не умеет", d.Version)
	}
	head := make([]byte, 8)
	if _, err := r.ReadAt(head, 0); err != nil {
		return err
	}
	encoded := head[3]&0x80 != 0
	info := append([]byte(nil), d.info...)
	be := binary.BigEndian
	pos := d.InfoOffset
	var tail [][]byte
	idx := make([]int, 0, len(replace))
	for i := range replace {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		data := replace[i]
		pad := (16 - pos%16) % 16
		tail = append(tail, make([]byte, pad), data)
		pos += pad
		e := d.Files[i].entry
		if d.Version <= -11 {
			be.PutUint64(info[e:], uint64(pos))
			e += 8
		} else {
			if pos > 0xFFFFFFFF {
				return fmt.Errorf("DAT: архив больше 4 ГБ")
			}
			be.PutUint32(info[e:], uint32(pos))
			e += 4
		}
		be.PutUint32(info[e:], uint32(len(data)))
		be.PutUint32(info[e+4:], uint32(len(data))) // без флага сжатия
		pos += int64(len(data))
	}
	// оглавление — с границы 256 байт плюс 0x100 (так записывается его смещение в начале архива)
	pad := (256 - (pos-0x100)%256 + 256) % 256
	tail = append(tail, make([]byte, pad))
	pos += pad
	le := binary.LittleEndian
	if encoded {
		le.PutUint32(head, uint32((pos-0x100)>>8)^0xFFFFFFFF)
	} else {
		if pos > 0x7FFFFFFF {
			return fmt.Errorf("DAT: смещение оглавления не помещается")
		}
		le.PutUint32(head, uint32(pos))
	}
	le.PutUint32(head[4:], uint32(len(info)))
	if _, err := w.Write(head); err != nil {
		return err
	}
	if _, err := io.Copy(w, io.NewSectionReader(r, 8, d.InfoOffset-8)); err != nil {
		return err
	}
	for _, t := range tail {
		if _, err := w.Write(t); err != nil {
			return err
		}
	}
	_, err := w.Write(info)
	return err
}

// Index — номер файла по имени (как в Files[i].Name) или -1.
func (d *DAT) Index(name string) int {
	for i, f := range d.Files {
		if f.Name == name {
			return i
		}
	}
	return -1
}
