package nx

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// В NCZ первые 0x4000 байт — исходный (зашифрованный) заголовок NCA,
// дальше идёт тело NCA в расшифрованном виде, сжатое zstd.
const nczHeaderSize = 0x4000

// NCZHeader читает только заголовок — его хватает, чтобы узнать тип контента
// без распаковки.
func NCZHeader(r io.ReaderAt) *io.SectionReader {
	return io.NewSectionReader(r, 0, nczHeaderSize)
}

// OpenNCZ возвращает ReaderAt, который выглядит как NCA с уже расшифрованным телом.
// Для потокового NCZ тело распаковывается во временный файл в tmpDir.
func OpenNCZ(r io.ReaderAt, size int64, tmpDir string, progress func(done, total int64)) (io.ReaderAt, func(), error) {
	hdr := make([]byte, 0x10)
	if _, err := r.ReadAt(hdr, nczHeaderSize); err != nil {
		return nil, nil, err
	}
	if string(hdr[:8]) != "NCZSECTN" {
		return nil, nil, fmt.Errorf("нет сигнатуры NCZSECTN")
	}
	count := int64(binary.LittleEndian.Uint64(hdr[8:]))
	var bodySize int64
	secs := make([]byte, count*0x40)
	if _, err := r.ReadAt(secs, nczHeaderSize+0x10); err != nil {
		return nil, nil, err
	}
	for i := int64(0); i < count; i++ {
		e := secs[i*0x40:]
		end := int64(binary.LittleEndian.Uint64(e[0:]) + binary.LittleEndian.Uint64(e[8:]))
		bodySize = max(bodySize, end-nczHeaderSize)
	}
	pos := nczHeaderSize + 0x10 + count*0x40

	magic := make([]byte, 8)
	if _, err := r.ReadAt(magic, pos); err != nil {
		return nil, nil, err
	}
	var body io.ReaderAt
	closer := func() {}
	if string(magic) == "NCZBLOCK" {
		b, err := openBlockBody(r, pos)
		if err != nil {
			return nil, nil, err
		}
		body = b
	} else {
		f, err := os.CreateTemp(tmpDir, "ncz-*.bin")
		if err != nil {
			return nil, nil, err
		}
		closer = func() { f.Close(); os.Remove(f.Name()) }
		dec, err := zstd.NewReader(io.NewSectionReader(r, pos, size-pos), zstd.WithDecoderConcurrency(0))
		if err != nil {
			closer()
			return nil, nil, err
		}
		_, err = io.Copy(f, &progressReader{r: dec, total: bodySize, fn: progress})
		dec.Close()
		if err != nil {
			closer()
			return nil, nil, fmt.Errorf("распаковка NCZ: %w", err)
		}
		body = f
	}

	head := make([]byte, nczHeaderSize)
	if _, err := r.ReadAt(head, 0); err != nil {
		closer()
		return nil, nil, err
	}
	return &splitReader{head: head, body: body}, closer, nil
}

type splitReader struct {
	head []byte
	body io.ReaderAt
}

func (s *splitReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	if off < int64(len(s.head)) {
		n = copy(p, s.head[off:])
		if n == len(p) {
			return n, nil
		}
		off += int64(n)
	}
	m, err := s.body.ReadAt(p[n:], off-int64(len(s.head)))
	return n + m, err
}

// Блочный NCZ: сжатые блоки фиксированного размера, можно читать с любого места.
type blockBody struct {
	r         io.ReaderAt
	blockSize int64
	total     int64
	offsets   []int64
	sizes     []int64
	dec       *zstd.Decoder

	mu    sync.Mutex
	cache map[int][]byte
	order []int
}

func openBlockBody(r io.ReaderAt, pos int64) (*blockBody, error) {
	h := make([]byte, 0x18)
	if _, err := r.ReadAt(h, pos); err != nil {
		return nil, err
	}
	exp := h[11]
	num := int(binary.LittleEndian.Uint32(h[12:]))
	total := int64(binary.LittleEndian.Uint64(h[16:]))
	if exp < 14 || exp > 32 || num <= 0 {
		return nil, fmt.Errorf("повреждённый заголовок NCZBLOCK")
	}
	list := make([]byte, num*4)
	if _, err := r.ReadAt(list, pos+0x18); err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	b := &blockBody{r: r, blockSize: 1 << exp, total: total, dec: dec, cache: map[int][]byte{}}
	off := pos + 0x18 + int64(num*4)
	for i := 0; i < num; i++ {
		sz := int64(binary.LittleEndian.Uint32(list[i*4:]))
		b.offsets = append(b.offsets, off)
		b.sizes = append(b.sizes, sz)
		off += sz
	}
	return b, nil
}

func (b *blockBody) block(i int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d, ok := b.cache[i]; ok {
		return d, nil
	}
	want := min(b.blockSize, b.total-int64(i)*b.blockSize)
	raw := make([]byte, b.sizes[i])
	if _, err := b.r.ReadAt(raw, b.offsets[i]); err != nil {
		return nil, err
	}
	data := raw
	if b.sizes[i] < want {
		var err error
		if data, err = b.dec.DecodeAll(raw, make([]byte, 0, want)); err != nil {
			return nil, fmt.Errorf("блок NCZ %d: %w", i, err)
		}
	}
	b.cache[i] = data
	b.order = append(b.order, i)
	if len(b.order) > 8 {
		delete(b.cache, b.order[0])
		b.order = b.order[1:]
	}
	return data, nil
}

func (b *blockBody) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		if off >= b.total {
			return n, io.EOF
		}
		i := int(off / b.blockSize)
		data, err := b.block(i)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], data[off-int64(i)*b.blockSize:])
		n += c
		off += int64(c)
	}
	return n, nil
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil && n > 0 {
		p.fn(p.done, p.total)
	}
	return n, err
}
