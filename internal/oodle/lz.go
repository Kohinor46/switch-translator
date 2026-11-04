package oodle

// LZ-кусок Kraken: потоки литералов, команд, смещений и длин, затем их исполнение.
// mode 0 — литералы хранятся разностью с байтом на последнем смещении, mode 1 — как есть.
func decodeLZ(mode int, src, dst []byte, pos, end int) error {
	if mode > 1 || len(src) < 13 {
		return errCorrupt
	}
	count := end - pos
	p, d := 0, pos
	if pos == 0 { // первые 8 байт всего потока — как есть
		copy(dst, src[:8])
		p, d = 8, 8
	}
	if src[p]&0x80 != 0 {
		return errCorrupt
	}
	lits, used, err := decodeBytes(src[p:], count)
	if err != nil {
		return err
	}
	p += used
	cmds, used, err := decodeBytes(src[p:], count)
	if err != nil {
		return err
	}
	p += used
	if len(src)-p < 3 {
		return errCorrupt
	}
	scale := 0
	var packedOffs, extra []byte
	if src[p]&0x80 != 0 {
		scale = int(src[p]) - 127
		p++
		packedOffs, used, err = decodeBytes(src[p:], len(cmds))
		if err != nil {
			return err
		}
		p += used
		if scale != 1 {
			extra, used, err = decodeBytes(src[p:], len(packedOffs))
			if err != nil || len(extra) != len(packedOffs) {
				return errCorrupt
			}
			p += used
		}
	} else {
		packedOffs, used, err = decodeBytes(src[p:], len(cmds))
		if err != nil {
			return err
		}
		p += used
	}
	packedLens, used, err := decodeBytes(src[p:], count>>2)
	if err != nil {
		return err
	}
	p += used
	offs, lens, err := unpackOffsets(src[p:], packedOffs, extra, scale, packedLens)
	if err != nil {
		return err
	}
	return runLZ(mode, dst, d, end, lits, cmds, offs, lens)
}

// unpackOffsets — смещения и длинные длины в двух битовых потоках (с начала и с конца src).
func unpackOffsets(src, packedOffs, extra []byte, scale int, packedLens []byte) ([]int, []int, error) {
	a := &msbReader{b: src}
	b := &msbReader{b: src, back: true}
	if b.peek() < 0x2000 {
		return nil, nil, errCorrupt
	}
	z := b.zeros()
	b.pos += z
	longCount := int(b.read(z+1)) - 1
	offs := make([]int, len(packedOffs))
	for i, v := range packedOffs {
		r := a
		if i&1 == 1 {
			r = b
		}
		if scale == 0 {
			offs[i] = -readDistance(r, int(v))
			continue
		}
		n := int(v >> 3)
		if n > 26 {
			return nil, nil, errCorrupt
		}
		offs[i] = 8 - ((8+int(v&7))<<n | int(r.read(n)))
	}
	if scale > 1 {
		for i := range offs {
			offs[i] = scale*offs[i] - int(extra[i])
		}
	}
	if longCount > 512 {
		return nil, nil, errCorrupt
	}
	long := make([]int, longCount)
	for i := range long {
		r := a
		if i&1 == 1 {
			r = b
		}
		bitsv := r.peek()
		if bitsv == 0 {
			return nil, nil, errCorrupt
		}
		z := r.zeros()
		if z > 12 {
			return nil, nil, errCorrupt
		}
		r.pos += z
		long[i] = int(r.read(z+7)) - 64
	}
	lens := make([]int, len(packedLens))
	k := 0
	for i, v := range packedLens {
		l := int(v)
		if l == 255 {
			if k >= len(long) {
				return nil, nil, errCorrupt
			}
			l = long[k] + 255
			k++
		}
		lens[i] = l + 3
	}
	if k != len(long) {
		return nil, nil, errCorrupt
	}
	// поток с начала и поток с конца не могут занимать одни и те же биты
	if a.pos+b.pos > 8*len(src) {
		return nil, nil, errCorrupt
	}
	return offs, lens, nil
}

func readDistance(r *msbReader, v int) int {
	if v < 0xF0 {
		n := v>>4 + 4
		t := int(r.read(n))
		return (1<<n|t)<<4 + v&0xF - 248
	}
	n := v - 0xF0 + 4
	t := int(r.read(n))
	rv := 8322816 + (1<<n|t)<<12
	return rv + int(r.read(12))
}

func runLZ(mode int, dst []byte, d, end int, lits, cmds []byte, offs, lens []int) error {
	recent := [7]int{3: -8, 4: -8, 5: -8}
	last := -8
	li, oi, ni := 0, 0, 0
	putLits := func(n int) bool {
		if n > len(lits)-li || n > end-d || d+last < 0 {
			return false
		}
		if mode == 1 {
			copy(dst[d:], lits[li:li+n])
		} else {
			for i := range n {
				dst[d+i] = lits[li+i] + dst[d+i+last]
			}
		}
		d += n
		li += n
		return true
	}
	for _, f := range cmds {
		litlen := int(f & 3)
		idx := int(f >> 6)
		ml := int(f>>2) & 0xF
		if litlen == 3 {
			if ni >= len(lens) {
				return errCorrupt
			}
			litlen = lens[ni]
			ni++
		}
		if oi < len(offs) {
			recent[6] = offs[oi]
		}
		if !putLits(litlen) {
			return errCorrupt
		}
		off := recent[idx+3]
		for k := idx + 3; k > 3; k-- {
			recent[k] = recent[k-1]
		}
		recent[3] = off
		last = off
		if idx == 3 {
			oi++
		}
		if d+off < 0 {
			return errCorrupt
		}
		n := ml + 2
		if ml == 15 {
			if ni >= len(lens) {
				return errCorrupt
			}
			n = 14 + lens[ni]
			ni++
		}
		if n > end-d {
			return errCorrupt
		}
		for i := range n {
			dst[d+i] = dst[d+off+i]
		}
		d += n
	}
	if oi != len(offs) || ni != len(lens) || end-d != len(lits)-li {
		return errCorrupt
	}
	if !putLits(end - d) {
		return errCorrupt
	}
	return nil
}
