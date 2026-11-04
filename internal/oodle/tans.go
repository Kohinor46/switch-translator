package oodle

import (
	"math/bits"
	"sort"
)

// Блоки tANS: таблица весов символов, затем пять состояний, которые по очереди
// читают биты из прямого и обратного потоков. Последние 5 байт — сами конечные состояния.

type tansEnt struct {
	sym  byte
	bits uint8
	w    uint32
}

func decodeTANS(src, out []byte) error {
	if len(src) < 8 || len(out) < 5 {
		return errCorrupt
	}
	br := &msbReader{b: src}
	if br.read(1) != 0 {
		return errCorrupt
	}
	lbits := int(br.read(2)) + 8
	a, b, err := tansTable(br, lbits)
	if err != nil {
		return err
	}
	p := br.bytePos()
	if p >= len(src) {
		return errCorrupt
	}
	lut := tansLUT(a, b, lbits)
	f := &lsbReader{b: src[p:]}
	r := &lsbReader{b: src[p:], back: true}
	var st [5]uint32
	st[0] = f.read(lbits)
	st[1] = r.read(lbits)
	st[2] = f.read(lbits)
	st[3] = r.read(lbits)
	st[4] = f.read(lbits)
	n := len(out) - 5
	o := 0
	for o < n {
		for _, rd := range [2]*lsbReader{f, r} {
			for k := 0; k < 5 && o < n; k++ {
				e := lut[st[k]]
				out[o] = e.sym
				o++
				st[k] = rd.read(int(e.bits)) + e.w
			}
			if o >= n {
				break
			}
		}
	}
	// прямой и обратный потоки не могут занимать одни и те же биты
	if f.pos+r.pos > 8*len(f.b) {
		return errCorrupt
	}
	for k, s := range st {
		if s > 0xFF {
			return errCorrupt
		}
		out[n+k] = byte(s)
	}
	return nil
}

// tansTable — символы с весом 1 (a) и с весом больше (b: символ<<16 | вес); сумма весов — 1<<lbits.
func tansTable(br *msbReader, lbits int) ([]byte, []uint32, error) {
	L := 1 << lbits
	var a []byte
	var b []uint32
	if br.read(1) != 0 {
		q := int(br.read(3))
		num := int(br.read(8)) + 1
		if num < 2 {
			return nil, nil, errCorrupt
		}
		fluff := readFluff(br, num)
		rice := golombLengths(br, num+fluff)
		ranges, err := symbolRanges(br, num, fluff, rice[num:])
		if err != nil {
			return nil, nil, err
		}
		avg, sum, i := 6, 0, 0
		for _, rg := range ranges {
			for s := rg[0]; s < rg[0]+rg[1]; s++ {
				extra := q + rice[i]
				i++
				if extra > 15 {
					return nil, nil, errCorrupt
				}
				v := int(br.read(extra)) + 1<<extra - 1<<q
				d4 := avg >> 2
				limit := 2 * d4
				if v <= limit {
					v = d4 + zigzag(v)
				}
				if limit > v {
					limit = v
				}
				v++
				avg += limit - d4
				if v == 1 {
					a = append(a, byte(s))
				} else {
					b = append(b, uint32(s)<<16|uint32(v))
				}
				sum += v
			}
		}
		if sum != L {
			return nil, nil, errCorrupt
		}
		return a, b, nil
	}
	var seen [256]bool
	count := int(br.read(3)) + 1
	maxDelta := int(br.read(bits.Len(uint(lbits))))
	if maxDelta == 0 || maxDelta > lbits {
		return nil, nil, errCorrupt
	}
	weight, total := 0, 0
	for range count {
		s := int(br.read(8))
		if seen[s] {
			return nil, nil, errCorrupt
		}
		weight += int(br.read(maxDelta))
		if weight == 0 {
			return nil, nil, errCorrupt
		}
		seen[s] = true
		if weight == 1 {
			a = append(a, byte(s))
		} else {
			b = append(b, uint32(s)<<16|uint32(weight))
		}
		total += weight
	}
	s := int(br.read(8))
	if seen[s] || L-total < weight || L-total <= 1 {
		return nil, nil, errCorrupt
	}
	b = append(b, uint32(s)<<16|uint32(L-total))
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return a, b, nil
}

// tansLUT — состояние → символ, сколько бит дочитать и что к ним прибавить.
// Символы с весом 1 — в конце таблицы, остальные раскладываются по четырём частям.
func tansLUT(a []byte, b []uint32, lbits int) []tansEnt {
	L := 1 << lbits
	lut := make([]tansEnt, L)
	left := L - len(a)
	sa, rem := left>>2, left&3
	var ptr [4]int
	sb := sa
	if rem > 0 {
		sb++
	}
	ptr[1] = sb
	sb += sa
	if rem > 1 {
		sb++
	}
	ptr[2] = sb
	sb += sa
	if rem > 2 {
		sb++
	}
	ptr[3] = sb
	for i, s := range a {
		lut[left+i] = tansEnt{sym: s, bits: uint8(lbits)}
	}
	put := func(j int, e tansEnt) bool {
		if ptr[j] >= L {
			return false
		}
		lut[ptr[j]] = e
		ptr[j]++
		return true
	}
	sum := 0
	for _, v := range b {
		weight := int(v & 0xFFFF)
		sym := byte(v >> 16)
		if weight > 4 {
			symBits := bits.Len(uint(weight)) - 1
			z := lbits - symBits
			e := tansEnt{sym: sym, bits: uint8(z), w: uint32((L - 1) & (weight << z))}
			add := uint32(1) << z
			x := 1<<(symBits+1) - weight
			for j := range 4 {
				y := (weight + ((sum - j - 1) & 3)) >> 2
				if x >= y {
					for range y {
						put(j, e)
						e.w += add
					}
					x -= y
				} else {
					for range x {
						put(j, e)
						e.w += add
					}
					z--
					add >>= 1
					e.bits = uint8(z)
					e.w = 0
					for range y - x {
						put(j, e)
						e.w += add
					}
					x = weight
				}
			}
		} else {
			mask := uint32(1<<weight-1) << (sum & 3)
			mask |= mask >> 4
			ww := weight
			for range weight {
				j := bits.TrailingZeros32(mask)
				mask &= mask - 1
				wb := bits.Len(uint(ww)) - 1
				put(j, tansEnt{sym: sym, bits: uint8(lbits - wb), w: uint32((L - 1) & (ww << (lbits - wb)))})
				ww++
			}
		}
		sum += weight
	}
	return lut
}
