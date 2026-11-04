package oodle

import "math/bits"

// msbReader — биты старшими вперёд; back — байты с конца к началу. За краем — нули.
type msbReader struct {
	b    []byte
	back bool
	pos  int // в битах
}

// byteAt — i-й байт потока b; back — считая с конца. За краем — ноль.
func byteAt(b []byte, back bool, i int) uint64 {
	if i < 0 || i >= len(b) {
		return 0
	}
	if back {
		return uint64(b[len(b)-1-i])
	}
	return uint64(b[i])
}

// peek — следующие 32 бита, первый — старший.
func (r *msbReader) peek() uint32 {
	i := r.pos >> 3
	var v uint64
	for k := range 5 {
		v = v<<8 | byteAt(r.b, r.back, i+k)
	}
	return uint32(v << (24 + r.pos&7) >> 32)
}

func (r *msbReader) read(n int) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek() >> (32 - n)
	r.pos += n
	return v
}

// zeros — сколько нулей до первой единицы в следующих 32 битах (32, если единицы нет).
func (r *msbReader) zeros() int { return bits.LeadingZeros32(r.peek()) }

// bytePos — сколько байт занято (неполный байт считается).
func (r *msbReader) bytePos() int { return (r.pos + 7) >> 3 }

// lsbReader — биты младшими вперёд; back — байты с конца к началу.
type lsbReader struct {
	b    []byte
	back bool
	pos  int
}

func (r *lsbReader) peek() uint32 {
	i := r.pos >> 3
	var v uint64
	for k := 4; k >= 0; k-- {
		v = v<<8 | byteAt(r.b, r.back, i+k)
	}
	return uint32(v >> (r.pos & 7))
}

func (r *lsbReader) read(n int) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek() & (1<<n - 1)
	r.pos += n
	return v
}

func zigzag(v int) int { return -(v & 1) ^ (v >> 1) }
