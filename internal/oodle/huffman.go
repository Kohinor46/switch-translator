package oodle

import (
	"encoding/binary"
	"math/bits"
)

// Блоки Хаффмана: таблица длин кодов (каноническая, до 11 бит), затем три потока —
// прямой с начала, обратный с конца и прямой с середины; символы идут из них по очереди.

type huffLUT struct {
	length [2048]uint8
	sym    [2048]byte
}

func decodeHuffman(src, out []byte, split bool) error {
	br := &msbReader{b: src}
	var buckets [12][]byte
	var err error
	switch {
	case br.read(1) == 0:
		err = huffLengthsOld(br, &buckets)
	case br.read(1) == 0:
		err = huffLengthsNew(br, &buckets)
	default:
		return errCorrupt
	}
	if err != nil {
		return err
	}
	p := br.bytePos()
	used, one := 0, -1
	for l := 1; l <= 11; l++ {
		used += len(buckets[l])
		if len(buckets[l]) == 1 {
			one = int(buckets[l][0])
		}
	}
	if used == 1 {
		for i := range out {
			out[i] = byte(one)
		}
		return nil
	}
	lut, ok := makeHuffLUT(&buckets)
	if !ok {
		return errCorrupt
	}
	if !split {
		if p+3 > len(src) {
			return errCorrupt
		}
		mid := int(binary.LittleEndian.Uint16(src[p:]))
		p += 2
		if p+mid > len(src) {
			return errCorrupt
		}
		huffStreams(src[p:], mid, out, lut)
		return nil
	}
	if p+6 > len(src) {
		return errCorrupt
	}
	half := (len(out) + 1) >> 1
	mid := int(src[p]) | int(src[p+1])<<8 | int(src[p+2])<<16
	p += 3
	if mid > len(src)-p {
		return errCorrupt
	}
	srcMid := p + mid
	left := int(binary.LittleEndian.Uint16(src[p:]))
	p += 2
	if srcMid-p < left+2 || len(src)-srcMid < 3 {
		return errCorrupt
	}
	right := int(binary.LittleEndian.Uint16(src[srcMid:]))
	if len(src)-(srcMid+2) < right+2 {
		return errCorrupt
	}
	huffStreams(src[p:srcMid], left, out[:half], lut)
	huffStreams(src[srcMid+2:], right, out[half:], lut)
	return nil
}

// huffStreams — три потока в b: [0, mid) вперёд, [mid, конец) с конца назад и с mid вперёд.
func huffStreams(b []byte, mid int, out []byte, lut *huffLUT) {
	rs := [3]*lsbReader{{b: b[:mid]}, {b: b[mid:], back: true}, {b: b[mid:]}}
	for i := range out {
		r := rs[i%3]
		k := r.peek() & 0x7FF
		out[i] = lut.sym[k]
		r.pos += int(lut.length[k])
	}
}

// makeHuffLUT — таблица на 11 бит для чтения младшими вперёд.
func makeHuffLUT(buckets *[12][]byte) (*huffLUT, bool) {
	lut := &huffLUT{}
	slot := 0
	for l := 1; l <= 11; l++ {
		step := 1 << (11 - l)
		for _, s := range buckets[l] {
			if slot+step > 2048 {
				return nil, false
			}
			code := slot >> (11 - l)
			rev := int(bits.Reverse16(uint16(code)) >> (16 - l))
			for ext := 0; ext < step; ext++ {
				k := rev | ext<<l
				lut.length[k] = uint8(l)
				lut.sym[k] = s
			}
			slot += step
		}
	}
	return lut, slot == 2048
}

// huffLengthsOld — старая запись длин: плотная (серии нулей и длины по Райсу
// относительно средней) или разреженная (символ и длина явно).
func huffLengthsOld(br *msbReader, buckets *[12][]byte) error {
	if br.read(1) == 0 {
		n := int(br.read(8))
		if n == 0 {
			return errCorrupt
		}
		if n == 1 {
			buckets[1] = append(buckets[1], byte(br.read(8)))
			return nil
		}
		lenBits := int(br.read(3))
		if lenBits > 4 {
			return errCorrupt
		}
		for range n {
			s := byte(br.read(8))
			l := int(br.read(lenBits)) + 1
			if l > 11 {
				return errCorrupt
			}
			buckets[l] = append(buckets[l], s)
		}
		return nil
	}
	forced := int(br.read(2))
	gamma := func() (int, bool) {
		if br.peek()&0xFF000000 == 0 {
			return 0, false
		}
		z := br.zeros()
		return int(br.read(2*(z+1))) - 1, true
	}
	sym, num, avg := 0, 0, 32
	skip := br.read(1) == 1 // таблица начинается без серии нулей
	for {
		if !skip {
			n, ok := gamma()
			if !ok {
				return errCorrupt
			}
			sym += n
			if sym >= 256 {
				break
			}
		}
		skip = false
		n, ok := gamma()
		if !ok || sym+n > 256 {
			return errCorrupt
		}
		num += n
		for range n {
			z := br.zeros()
			if z >= 24 {
				return errCorrupt
			}
			v := int(br.read(z+forced+1)) + (z-1)<<forced
			l := zigzag(v) + (avg+2)>>2
			if l < 1 || l > 11 {
				return errCorrupt
			}
			avg = l + (3*avg+2)>>2
			buckets[l] = append(buckets[l], byte(sym))
			sym++
		}
		if sym == 256 {
			break
		}
	}
	if sym != 256 || num < 2 {
		return errCorrupt
	}
	return nil
}

// huffLengthsNew — новая запись: число символов, их расположение отрезками и длины кодами Голомба–Райса.
func huffLengthsNew(br *msbReader, buckets *[12][]byte) error {
	forced := int(br.read(2))
	num := int(br.read(8)) + 1
	fluff := readFluff(br, num)
	vals := golombLengths(br, num+fluff)
	for i := range num {
		vals[i] = vals[i]<<forced | int(br.read(forced))
	}
	lens := make([]int, num)
	sum := 0x1E
	for i := range num {
		v := zigzag(vals[i])
		lens[i] = v + sum>>2 + 1
		if lens[i] < 1 || lens[i] > 11 {
			return errCorrupt
		}
		sum += v
	}
	ranges, err := symbolRanges(br, num, fluff, vals[num:])
	if err != nil {
		return err
	}
	i := 0
	for _, rg := range ranges {
		for s := rg[0]; s < rg[0]+rg[1]; s++ {
			buckets[lens[i]] = append(buckets[lens[i]], byte(s))
			i++
		}
	}
	return nil
}

// readFluff — число отрезков (усечённый двоичный код).
func readFluff(br *msbReader, num int) int {
	if num == 256 {
		return 0
	}
	x := min(257-num, num) * 2
	y := bits.Len32(uint32(x - 1))
	v := int(br.peek() >> (32 - y))
	z := 1<<y - x
	if v>>1 >= z {
		br.pos += y
		return v - z
	}
	br.pos += y - 1
	return v >> 1
}

// golombLengths — n унарных чисел (нули до единицы).
func golombLengths(br *msbReader, n int) []int {
	out := make([]int, n)
	for i := range out {
		z := 0
		for br.read(1) == 0 {
			z++
			if z > 4096 {
				break
			}
		}
		out[i] = z
	}
	return out
}

// symbolRanges — какие символы есть: отрезки [начало, число], разделённые пропусками.
func symbolRanges(br *msbReader, num, fluff int, lens []int) ([][2]int, error) {
	n := fluff >> 1
	sym := 0
	if fluff&1 != 0 {
		v := lens[0]
		lens = lens[1:]
		if v >= 8 {
			return nil, errCorrupt
		}
		sym = int(br.read(v+1)) + 1<<(v+1) - 1
	}
	used := 0
	var out [][2]int
	for range n {
		v := lens[0]
		if v >= 9 {
			return nil, errCorrupt
		}
		cnt := int(br.read(v)) + 1<<v
		v = lens[1]
		if v >= 8 {
			return nil, errCorrupt
		}
		space := int(br.read(v+1)) + 1<<(v+1) - 1
		out = append(out, [2]int{sym, cnt})
		used += cnt
		sym += cnt + space
		lens = lens[2:]
	}
	if sym >= 256 || used >= num || sym+num-used > 256 {
		return nil, errCorrupt
	}
	return append(out, [2]int{sym, num - used}), nil
}
