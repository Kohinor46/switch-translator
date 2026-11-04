package unity

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"switch-translator/internal/codec"
)

// Бандл UnityFS: заголовок, оглавление (блоки и узлы-файлы) и данные блоками, каждый сжат
// по-своему (обычно LZ4/LZ4HC). Узлы — сериализованные файлы CAB-… и их данные CAB-….resource / .resS.

// Node — файл внутри бандла.
type Node struct {
	Offset, Size int64 // в распакованных данных
	Flags        uint32
	Path         string
}

type block struct {
	usize, csize uint32
	flags        uint16
	start        int64 // где сжатый блок в файле
	ustart       int64 // где он в распакованных данных
}

// Bundle — оглавление бандла.
type Bundle struct {
	Version    uint32
	Unity, Rev string
	flags      uint32
	hash       []byte
	blocks     []block
	Nodes      []Node
	headerEnd  int64
}

// IsBundle — начинается ли с «UnityFS».
func IsBundle(head []byte) bool { return bytes.HasPrefix(head, []byte("UnityFS\x00")) }

// OpenBundle читает оглавление бандла.
func OpenBundle(r io.ReaderAt, size int64) (*Bundle, error) {
	head := make([]byte, min(size, 512))
	if _, err := r.ReadAt(head, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if !IsBundle(head) {
		return nil, fmt.Errorf("не бандл UnityFS")
	}
	be := binary.BigEndian
	p := 8
	b := &Bundle{}
	b.Version = be.Uint32(head[p:])
	p += 4
	cstr := func() string {
		i := bytes.IndexByte(head[p:], 0)
		if i < 0 {
			return ""
		}
		s := string(head[p : p+i])
		p += i + 1
		return s
	}
	b.Unity, b.Rev = cstr(), cstr()
	if p+20 > len(head) {
		return nil, fmt.Errorf("бандл: заголовок обрывается")
	}
	p += 8 // размер файла
	csize := int(be.Uint32(head[p:]))
	usize := int(be.Uint32(head[p+4:]))
	b.flags = be.Uint32(head[p+8:])
	p += 12
	if b.Version >= 7 {
		p = (p + 15) &^ 15
	}
	infoAt := int64(p)
	if b.flags&0x80 != 0 {
		infoAt = size - int64(csize)
	}
	raw := make([]byte, csize)
	if _, err := r.ReadAt(raw, infoAt); err != nil && err != io.EOF {
		return nil, err
	}
	info, err := decompress(raw, usize, b.flags&0x3F)
	if err != nil {
		return nil, fmt.Errorf("бандл: оглавление: %w", err)
	}
	dataAt := int64(p)
	if b.flags&0x80 == 0 {
		dataAt += int64(csize)
	}
	if b.flags&0x200 != 0 { // выравнивание данных (Unity 2020+)
		dataAt = (dataAt + 15) &^ 15
	}
	b.headerEnd = int64(p)
	q := 0
	b.hash = info[:16]
	q = 16
	nb := int(be.Uint32(info[q:]))
	q += 4
	start, ustart := dataAt, int64(0)
	for range nb {
		if q+10 > len(info) {
			return nil, fmt.Errorf("бандл: оглавление обрывается")
		}
		bl := block{usize: be.Uint32(info[q:]), csize: be.Uint32(info[q+4:]), flags: be.Uint16(info[q+8:]), start: start, ustart: ustart}
		q += 10
		start += int64(bl.csize)
		ustart += int64(bl.usize)
		b.blocks = append(b.blocks, bl)
	}
	nn := int(be.Uint32(info[q:]))
	q += 4
	for range nn {
		if q+20 > len(info) {
			return nil, fmt.Errorf("бандл: узлы обрываются")
		}
		n := Node{Offset: int64(be.Uint64(info[q:])), Size: int64(be.Uint64(info[q+8:])), Flags: be.Uint32(info[q+16:])}
		q += 20
		i := bytes.IndexByte(info[q:], 0)
		if i < 0 {
			return nil, fmt.Errorf("бандл: имя узла без конца")
		}
		n.Path = string(info[q : q+i])
		q += i + 1
		b.Nodes = append(b.Nodes, n)
	}
	return b, nil
}

func decompress(b []byte, usize int, method uint32) ([]byte, error) {
	switch method {
	case 0:
		return b, nil
	case 2, 3:
		return codec.LZ4Block(b, usize)
	}
	return nil, fmt.Errorf("сжатие %d (LZMA) программа не распаковывает", method)
}

// ReadNode — содержимое узла: распаковываются только блоки, на которые он приходится.
func (b *Bundle) ReadNode(r io.ReaderAt, n Node) ([]byte, error) {
	out := make([]byte, 0, n.Size)
	end := n.Offset + n.Size
	for _, bl := range b.blocks {
		bEnd := bl.ustart + int64(bl.usize)
		if bEnd <= n.Offset || bl.ustart >= end {
			continue
		}
		raw := make([]byte, bl.csize)
		if _, err := r.ReadAt(raw, bl.start); err != nil && err != io.EOF {
			return nil, err
		}
		data, err := decompress(raw, int(bl.usize), uint32(bl.flags&0x3F))
		if err != nil {
			return nil, err
		}
		from, to := max(n.Offset, bl.ustart)-bl.ustart, min(end, bEnd)-bl.ustart
		out = append(out, data[from:to]...)
	}
	if int64(len(out)) != n.Size {
		return nil, fmt.Errorf("бандл: узел %s прочитан не целиком", n.Path)
	}
	return out, nil
}

// Rebuild — бандл без сжатия с заменёнными узлами (путь → содержимое); остальные узлы — как были.
func (b *Bundle) Rebuild(r io.ReaderAt, replace map[string][]byte) ([]byte, error) {
	be := binary.BigEndian
	var data []byte
	var nodes []Node
	for _, n := range b.Nodes {
		d, ok := replace[n.Path]
		if !ok {
			var err error
			if d, err = b.ReadNode(r, n); err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, Node{Offset: int64(len(data)), Size: int64(len(d)), Flags: n.Flags, Path: n.Path})
		data = append(data, d...)
	}
	// оглавление: один блок без сжатия на каждые 128 КБ данных
	const blockSize = 128 << 10
	var info []byte
	info = append(info, make([]byte, 16)...)
	nblocks := (len(data) + blockSize - 1) / blockSize
	info = be.AppendUint32(info, uint32(nblocks))
	for i := range nblocks {
		sz := min(blockSize, len(data)-i*blockSize)
		info = be.AppendUint32(info, uint32(sz))
		info = be.AppendUint32(info, uint32(sz))
		info = be.AppendUint16(info, 0)
	}
	info = be.AppendUint32(info, uint32(len(nodes)))
	for _, n := range nodes {
		info = be.AppendUint64(info, uint64(n.Offset))
		info = be.AppendUint64(info, uint64(n.Size))
		info = be.AppendUint32(info, n.Flags)
		info = append(info, n.Path...)
		info = append(info, 0)
	}
	var out []byte
	out = append(out, "UnityFS\x00"...)
	out = be.AppendUint32(out, b.Version)
	out = append(out, b.Unity...)
	out = append(out, 0)
	out = append(out, b.Rev...)
	out = append(out, 0)
	sizeAt := len(out)
	out = be.AppendUint64(out, 0)
	out = be.AppendUint32(out, uint32(len(info)))
	out = be.AppendUint32(out, uint32(len(info)))
	flags := b.flags&0x40 | b.flags&0x200 // оглавление сразу за заголовком, без сжатия
	out = be.AppendUint32(out, flags)
	if b.Version >= 7 {
		for len(out)%16 != 0 {
			out = append(out, 0)
		}
	}
	out = append(out, info...)
	if flags&0x200 != 0 {
		for len(out)%16 != 0 {
			out = append(out, 0)
		}
	}
	out = append(out, data...)
	be.PutUint64(out[sizeAt:], uint64(len(out)))
	return out, nil
}
