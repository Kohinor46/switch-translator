// Package oodle распаковывает данные, сжатые Oodle Kraken (так сжаты файлы во многих .pak
// Unreal Engine 4 на Switch). Только распаковка и только Kraken; Mermaid, Selkie, Leviathan
// и прочие методы Oodle программа не знает.
//
// Устройство формата известно по открытому разбору ooz (Powzix); здесь он описан заново на Go.
package oodle

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var errCorrupt = errors.New("Oodle: испорченные данные")

// Decompress распаковывает поток src в rawSize байт.
func Decompress(src []byte, rawSize int) ([]byte, error) {
	if rawSize < 0 {
		return nil, errCorrupt
	}
	dst := make([]byte, rawSize)
	var uncompressed, checksums bool
	sp := 0
	for pos := 0; pos < rawSize; {
		if pos&0x3FFFF == 0 {
			// заголовок каждых 256 КБ: младшая тетрада 0xC, бит 6 — без сжатия, байт 2 — метод
			if sp+2 > len(src) {
				return nil, errCorrupt
			}
			if src[sp]&0x0F != 0x0C || (src[sp]>>4)&3 != 0 {
				return nil, fmt.Errorf("Oodle: нет заголовка блока (%02x)", src[sp])
			}
			uncompressed = src[sp]&0x40 != 0
			checksums = src[sp+1]&0x80 != 0
			if t := src[sp+1] & 0x7F; t != 6 {
				return nil, fmt.Errorf("Oodle: метод %d программа не распаковывает (только Kraken)", t)
			}
			sp += 2
		}
		n := min(rawSize-pos, 0x40000)
		if uncompressed {
			if sp+n > len(src) {
				return nil, errCorrupt
			}
			copy(dst[pos:], src[sp:sp+n])
			sp += n
			pos += n
			continue
		}
		// заголовок кванта: 3 байта, размер сжатых данных минус один; 0x3FFFF — особые случаи
		if sp+3 > len(src) {
			return nil, errCorrupt
		}
		v := int(src[sp])<<16 | int(src[sp+1])<<8 | int(src[sp+2])
		sp += 3
		size := v & 0x3FFFF
		if size == 0x3FFFF {
			if v>>18 != 1 || sp >= len(src) {
				return nil, errCorrupt
			}
			// весь квант — один повторённый байт
			for i := range n {
				dst[pos+i] = src[sp]
			}
			sp++
			pos += n
			continue
		}
		size++
		if checksums { // контрольная сумма кванта не проверяется
			sp += 3
		}
		if size > n || sp+size > len(src) {
			return nil, errCorrupt
		}
		if size == n {
			copy(dst[pos:], src[sp:sp+n])
		} else if err := decodeQuantum(dst, pos, pos+n, src[sp:sp+size]); err != nil {
			return nil, err
		}
		sp += size
		pos += n
	}
	return dst, nil
}

// decodeQuantum — квант до 256 КБ: куски по 128 КБ, каждый либо только энтропийный,
// либо LZ (литералы, команды, смещения, длины).
func decodeQuantum(dst []byte, pos, end int, src []byte) error {
	for pos < end {
		count := min(end-pos, 0x20000)
		if len(src) < 4 {
			return errCorrupt
		}
		hdr := int(src[0])<<16 | int(src[1])<<8 | int(src[2])
		if hdr&0x800000 == 0 {
			out, used, err := decodeBytes(src, count)
			if err != nil {
				return err
			}
			if len(out) != count {
				return errCorrupt
			}
			copy(dst[pos:], out)
			src = src[used:]
		} else {
			src = src[3:]
			used := hdr & 0x7FFFF
			mode := (hdr >> 19) & 0xF
			if used > len(src) {
				return errCorrupt
			}
			switch {
			case used < count:
				if err := decodeLZ(mode, src[:used], dst, pos, pos+count); err != nil {
					return err
				}
			case used == count && mode == 0:
				copy(dst[pos:], src[:count])
			default:
				return errCorrupt
			}
			src = src[used:]
		}
		pos += count
	}
	return nil
}

// decodeBytes — энтропийный блок: заголовок с типом и размерами, затем данные.
// Возвращает распакованное и сколько байт src занято.
func decodeBytes(src []byte, maxOut int) ([]byte, int, error) {
	if len(src) < 2 {
		return nil, 0, errCorrupt
	}
	typ := (src[0] >> 4) & 7
	if typ == 0 { // как есть
		var n, h int
		if src[0] >= 0x80 {
			n, h = (int(src[0])<<8|int(src[1]))&0xFFF, 2
		} else {
			if len(src) < 3 {
				return nil, 0, errCorrupt
			}
			n, h = int(src[0])<<16|int(src[1])<<8|int(src[2]), 3
			if n&^0x3FFFF != 0 {
				return nil, 0, errCorrupt
			}
		}
		if n > maxOut || h+n > len(src) {
			return nil, 0, errCorrupt
		}
		return src[h : h+n], h + n, nil
	}
	var srcSize, dstSize, h int
	if src[0] >= 0x80 { // короткие размеры по 10 бит
		if len(src) < 3 {
			return nil, 0, errCorrupt
		}
		bits := int(src[0])<<16 | int(src[1])<<8 | int(src[2])
		srcSize = bits & 0x3FF
		dstSize = srcSize + (bits>>10)&0x3FF + 1
		h = 3
	} else { // длинные по 18 бит
		if len(src) < 5 {
			return nil, 0, errCorrupt
		}
		bits := int(binary.BigEndian.Uint32(src[1:]))
		srcSize = bits & 0x3FFFF
		dstSize = ((bits>>18)|int(src[0])<<14)&0x3FFFF + 1
		if srcSize >= dstSize {
			return nil, 0, errCorrupt
		}
		h = 5
	}
	if h+srcSize > len(src) || dstSize > maxOut {
		return nil, 0, errCorrupt
	}
	body := src[h : h+srcSize]
	out := make([]byte, dstSize)
	var err error
	switch typ {
	case 1:
		err = decodeTANS(body, out)
	case 2:
		err = decodeHuffman(body, out, false)
	case 4:
		err = decodeHuffman(body, out, true)
	case 3:
		err = decodeRLE(body, out)
	case 5:
		err = decodeRecursive(body, out)
	default:
		err = fmt.Errorf("Oodle: тип блока %d", typ)
	}
	if err != nil {
		return nil, 0, err
	}
	return out, h + srcSize, nil
}

// blockSize — сколько байт даст следующий энтропийный блок (не распаковывая его).
func blockSize(src []byte) (int, error) {
	if len(src) < 2 {
		return 0, errCorrupt
	}
	typ := (src[0] >> 4) & 7
	switch {
	case typ == 0 && src[0] >= 0x80:
		return (int(src[0])<<8 | int(src[1])) & 0xFFF, nil
	case typ == 0:
		if len(src) < 3 {
			return 0, errCorrupt
		}
		return int(src[0])<<16 | int(src[1])<<8 | int(src[2]), nil
	case typ >= 6:
		return 0, errCorrupt
	case src[0] >= 0x80:
		if len(src) < 3 {
			return 0, errCorrupt
		}
		bits := int(src[0])<<16 | int(src[1])<<8 | int(src[2])
		return bits&0x3FF + (bits>>10)&0x3FF + 1, nil
	default:
		if len(src) < 5 {
			return 0, errCorrupt
		}
		bits := int(binary.BigEndian.Uint32(src[1:]))
		return ((bits>>18)|int(src[0])<<14)&0x3FFFF + 1, nil
	}
}

// decodeRecursive — блок из нескольких блоков подряд либо «мультимассив».
func decodeRecursive(src, out []byte) error {
	if len(src) < 6 {
		return errCorrupt
	}
	n := int(src[0] & 0x7F)
	if n < 2 {
		return errCorrupt
	}
	if src[0]&0x80 == 0 {
		p, o := 1, 0
		for range n {
			b, used, err := decodeBytes(src[p:], len(out)-o)
			if err != nil {
				return err
			}
			o += copy(out[o:], b)
			p += used
		}
		if o != len(out) || p != len(src) {
			return errCorrupt
		}
		return nil
	}
	used, size, err := decodeMultiArray(src, out)
	if err != nil {
		return err
	}
	if size != len(out) || used != len(src) {
		return errCorrupt
	}
	return nil
}

// decodeMultiArray — несколько энтропийных массивов и список отрезков: из какого массива
// и сколько байт взять по порядку. Возвращает занятые байты src и длину результата.
func decodeMultiArray(src, out []byte) (int, int, error) {
	if len(src) < 4 {
		return 0, 0, errCorrupt
	}
	p := 0
	numArrays := int(src[p])
	p++
	if numArrays&0x80 == 0 {
		return 0, 0, errCorrupt
	}
	numArrays &= 0x3F
	if numArrays == 0 {
		b, used, err := decodeBytes(src[p:], len(out))
		if err != nil {
			return 0, 0, err
		}
		copy(out, b)
		return p + used, len(b), nil
	}
	arrays := make([][]byte, numArrays)
	for i := range arrays {
		b, used, err := decodeBytes(src[p:], 1<<20)
		if err != nil {
			return 0, 0, err
		}
		arrays[i] = b
		p += used
	}
	if len(src)-p < 3 {
		return 0, 0, errCorrupt
	}
	q := int(binary.LittleEndian.Uint16(src[p:]))
	p += 2
	numIndexes, err := blockSize(src[p:])
	if err != nil {
		return 0, 0, err
	}
	numLens := numIndexes - 1
	if numLens < 1 {
		return 0, 0, errCorrupt
	}
	var indexes, lenlog2 []byte
	if q&0x8000 != 0 {
		b, used, err := decodeBytes(src[p:], numIndexes)
		if err != nil || len(b) != numIndexes {
			return 0, 0, errCorrupt
		}
		p += used
		indexes = make([]byte, numIndexes)
		lenlog2 = make([]byte, numIndexes)
		for i, t := range b {
			lenlog2[i] = t >> 4
			indexes[i] = t & 0xF
		}
		numLens = numIndexes
	} else {
		b, used, err := decodeBytes(src[p:], numIndexes)
		if err != nil || len(b) != numIndexes {
			return 0, 0, errCorrupt
		}
		p += used
		indexes = b
		b, used, err = decodeBytes(src[p:], numIndexes)
		if err != nil || len(b) != numLens {
			return 0, 0, errCorrupt
		}
		p += used
		lenlog2 = b
		for _, v := range lenlog2 {
			if v > 16 {
				return 0, 0, errCorrupt
			}
		}
	}
	varLen := q & 0x3FFF
	if len(src)-p < varLen {
		return 0, 0, errCorrupt
	}
	// длины отрезков: по очереди из прямого и обратного потоков, старшими битами вперёд,
	// у каждого значения неявная старшая единица
	vb := src[p : p+varLen]
	f := &msbReader{b: vb}
	r := &msbReader{b: vb, back: true}
	lens := make([]int, numLens)
	for i := range lens {
		br := f
		if i&1 == 1 {
			br = r
		}
		n := int(lenlog2[i])
		lens[i] = 1<<n | int(br.read(n))
	}
	if indexes[numIndexes-1] != 0 {
		return 0, 0, errCorrupt
	}
	o, indi, leni := 0, 0, 0
	// массив на выходе один (у Kraken так всегда в этом месте)
	for {
		if indi >= numIndexes {
			return 0, 0, errCorrupt
		}
		s := int(indexes[indi])
		indi++
		if s == 0 {
			break
		}
		if s > numArrays || leni >= numLens {
			return 0, 0, errCorrupt
		}
		l := lens[leni]
		leni++
		if l > len(arrays[s-1]) || l > len(out)-o {
			return 0, 0, errCorrupt
		}
		o += copy(out[o:], arrays[s-1][:l])
		arrays[s-1] = arrays[s-1][l:]
	}
	if q&0x8000 != 0 {
		leni++
	}
	if indi != numIndexes || leni != numLens {
		return 0, 0, errCorrupt
	}
	for _, a := range arrays {
		if len(a) != 0 {
			return 0, 0, errCorrupt
		}
	}
	return p + varLen, o, nil
}

// decodeRLE — повторы байта и вставки как есть; команды читаются с конца.
func decodeRLE(src, out []byte) error {
	if len(src) <= 1 {
		if len(src) != 1 {
			return errCorrupt
		}
		for i := range out {
			out[i] = src[0]
		}
		return nil
	}
	cmd := src[1:]
	if src[0] != 0 {
		// начало команд сжато энтропийным блоком, остаток лежит как есть
		b, used, err := decodeBytes(src, 1<<20)
		if err != nil {
			return err
		}
		cmd = append(append([]byte(nil), b...), src[used:]...)
	}
	lo, hi, o := 0, len(cmd), 0
	var rle byte
	put := func(copyN, rleN int) error {
		if len(out)-o < copyN+rleN || hi-lo < copyN {
			return errCorrupt
		}
		o += copy(out[o:], cmd[lo:lo+copyN])
		lo += copyN
		for range rleN {
			out[o] = rle
			o++
		}
		return nil
	}
	for lo < hi {
		c := int(cmd[hi-1])
		var err error
		switch {
		case c-1 >= 0x2F || c == 0:
			hi--
			err = put((-1-c)&0xF, c>>4)
		case c >= 0x10:
			if hi-2 < lo {
				return errCorrupt
			}
			d := int(binary.LittleEndian.Uint16(cmd[hi-2:])) - 4096
			hi -= 2
			err = put(d&0x3F, d>>6)
		case c == 1:
			rle = cmd[lo]
			lo++
			hi--
		case c >= 9:
			if hi-2 < lo {
				return errCorrupt
			}
			n := (int(binary.LittleEndian.Uint16(cmd[hi-2:])) - 0x8FF) * 128
			hi -= 2
			err = put(0, n)
		default:
			if hi-2 < lo {
				return errCorrupt
			}
			n := (int(binary.LittleEndian.Uint16(cmd[hi-2:])) - 511) * 64
			hi -= 2
			err = put(n, 0)
		}
		if err != nil {
			return err
		}
	}
	if lo != hi || o != len(out) {
		return errCorrupt
	}
	return nil
}
