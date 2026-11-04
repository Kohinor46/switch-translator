package nx

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
)

// IVFC — дерево хэшей над RomFS: шесть уровней, блок 0x4000. Каждый уровень хэшей хранит SHA-256
// блоков следующего и, как у Nintendo, дополнен нулями до целого блока; последний блок данных
// хэшируется дополненным нулями. Главный хэш (от первого уровня) лежит в FS-заголовке.

const ivfcBlock = 0x4000
const ivfcLevels = 6

// ivfcHasher считает хэши блоков данных на лету, пока RomFS пишется.
type ivfcHasher struct {
	w      io.Writer
	buf    []byte
	hashes []byte
	size   int64
}

func newIVFCHasher(w io.Writer) *ivfcHasher {
	return &ivfcHasher{w: w, buf: make([]byte, 0, ivfcBlock)}
}

func (h *ivfcHasher) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.size += int64(n)
	for q := p[:n]; len(q) > 0; {
		k := min(len(q), ivfcBlock-len(h.buf))
		h.buf = append(h.buf, q[:k]...)
		q = q[k:]
		if len(h.buf) == ivfcBlock {
			s := sha256.Sum256(h.buf)
			h.hashes = append(h.hashes, s[:]...)
			h.buf = h.buf[:0]
		}
	}
	return n, err
}

// hashBlocks — хэши блоков, последний дополнен нулями; результат дополнен нулями до блока.
func hashBlocks(data []byte) []byte {
	var out []byte
	for off := 0; off < len(data); off += ivfcBlock {
		blk := data[off:min(off+ivfcBlock, len(data))]
		if len(blk) < ivfcBlock {
			blk = append(append([]byte(nil), blk...), make([]byte, ivfcBlock-len(blk))...)
		}
		s := sha256.Sum256(blk)
		out = append(out, s[:]...)
	}
	return padTo(out, ivfcBlock)
}

func padTo(b []byte, a int) []byte {
	if r := len(b) % a; r != 0 || len(b) == 0 {
		b = append(b, make([]byte, a-r)...)
	}
	return b
}

// ivfcTree — готовые уровни хэшей и заголовок.
type ivfcTree struct {
	levels   [][]byte // уровни 1–5 (уже дополненные до блока)
	dataSize int64
	master   [32]byte
}

// finish дописывает последний неполный блок и строит верхние уровни.
func (h *ivfcHasher) finish() *ivfcTree {
	if len(h.buf) > 0 || h.size == 0 {
		blk := append(h.buf, make([]byte, ivfcBlock-len(h.buf))...)
		s := sha256.Sum256(blk)
		h.hashes = append(h.hashes, s[:]...)
	}
	t := &ivfcTree{dataSize: h.size}
	lvl := padTo(h.hashes, ivfcBlock) // уровень 5
	t.levels = [][]byte{lvl}
	for range ivfcLevels - 2 {
		lvl = hashBlocks(lvl)
		t.levels = append([][]byte{lvl}, t.levels...)
	}
	t.master = sha256.Sum256(t.levels[0])
	return t
}

// hashSize — сколько места занимают уровни хэшей перед данными.
func (t *ivfcTree) hashSize() int64 {
	n := int64(0)
	for _, l := range t.levels {
		n += int64(len(l))
	}
	return n
}

// fsHeader — FS-заголовок секции RomFS (без шифрования — его выставляет сборщик NCA).
func (t *ivfcTree) fsHeader() []byte {
	fh := make([]byte, 0x200)
	le := binary.LittleEndian
	le.PutUint16(fh[0:], 2)
	fh[2] = fsRomFS
	fh[3] = 3 // HierarchicalIntegrity
	h := fh[0x8:]
	copy(h, "IVFC")
	le.PutUint32(h[4:], 0x20000)
	le.PutUint32(h[8:], 0x20)
	le.PutUint32(h[0xC:], ivfcLevels+1)
	off := int64(0)
	for i := range ivfcLevels {
		size := t.dataSize
		if i < len(t.levels) {
			size = int64(len(t.levels[i]))
		}
		e := h[0x10+i*0x18:]
		le.PutUint64(e[0:], uint64(off))
		le.PutUint64(e[8:], uint64(size))
		le.PutUint32(e[16:], 14)
		off += size
	}
	copy(h[0xC0:], t.master[:])
	return fh
}

// sha256Layers — FS-заголовок и таблица хэшей для HierarchicalSha256 (PFS0 в Meta NCA):
// хэши блоков по 0x1000, последний блок — сколько есть.
func sha256Layers(data []byte) (fh []byte, table []byte, dataOff int64) {
	const block = 0x1000
	for off := 0; off < len(data); off += block {
		s := sha256.Sum256(data[off:min(off+block, len(data))])
		table = append(table, s[:]...)
	}
	dataOff = int64(len(table)) // как у Nintendo: данные сразу за таблицей, без выравнивания
	fh = make([]byte, 0x200)
	le := binary.LittleEndian
	le.PutUint16(fh[0:], 2)
	fh[2] = fsPFS0
	fh[3] = 2 // HierarchicalSha256
	h := fh[0x8:]
	master := sha256.Sum256(table)
	copy(h, master[:])
	le.PutUint32(h[0x20:], block)
	le.PutUint32(h[0x24:], 2)
	le.PutUint64(h[0x28:], 0)
	le.PutUint64(h[0x30:], uint64(len(table)))
	le.PutUint64(h[0x38:], uint64(dataOff))
	le.PutUint64(h[0x40:], uint64(len(data)))
	return fh, table, dataOff
}
