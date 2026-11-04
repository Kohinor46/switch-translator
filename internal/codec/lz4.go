package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// LZ4: кадр LZ4 (магия 04 22 4D 18). В Kirby and the Forgotten Land файлы *.cmpbin — кадр LZ4,
// перед которым записан размер распакованного (обёртка «lz4s»); голый кадр — обёртка «lz4».

var lz4Magic = []byte{0x04, 0x22, 0x4D, 0x18}

func lz4Wrapper(head []byte) string {
	switch {
	case bytes.HasPrefix(head, lz4Magic):
		return "lz4"
	case len(head) >= 8 && bytes.Equal(head[4:8], lz4Magic):
		return "lz4s"
	}
	return ""
}

// LZ4 распаковывает кадр LZ4 (с размером впереди или без). limit >= 0 — остановиться после limit байт;
// тогда и обрезанный вход не ошибка (подглядывание сигнатуры по началу файла).
func LZ4(src []byte, limit int) ([]byte, error) {
	var out []byte
	if lz4Wrapper(src) == "lz4s" {
		if n := int(binary.LittleEndian.Uint32(src)); limit < 0 && n > 0 && n < 1<<31 {
			out = make([]byte, 0, n)
		}
		src = src[4:]
	}
	if len(src) < 7 || !bytes.HasPrefix(src, lz4Magic) {
		return nil, fmt.Errorf("lz4: нет заголовка кадра")
	}
	flg := src[4]
	if flg>>6 != 1 {
		return nil, fmt.Errorf("lz4: версия кадра %d", flg>>6)
	}
	p := 6 // магия, FLG, BD
	if flg&0x08 != 0 {
		p += 8 // размер содержимого
	}
	if flg&0x01 != 0 {
		p += 4 // словарь
	}
	p++ // контрольная сумма заголовка
	blockSum := flg&0x10 != 0
	done := func() bool { return limit >= 0 && len(out) >= limit }
	for !done() {
		if p+4 > len(src) {
			if limit >= 0 {
				break
			}
			return nil, fmt.Errorf("lz4: кадр обрывается")
		}
		n := binary.LittleEndian.Uint32(src[p:])
		p += 4
		if n == 0 {
			break // конец кадра
		}
		size := int(n & 0x7FFFFFFF)
		end := p + size
		if end > len(src) {
			if limit < 0 {
				return nil, fmt.Errorf("lz4: блок обрывается")
			}
			end = len(src)
		}
		if n&0x80000000 != 0 {
			out = append(out, src[p:end]...)
		} else {
			var err error
			if out, err = lz4Block(src[p:end], out, limit); err != nil && limit < 0 {
				return nil, err
			}
		}
		p = end
		if blockSum {
			p += 4
		}
	}
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// lz4Block дописывает к out распакованный блок LZ4 (ссылки могут вести и в прошлые блоки — out общий).
func lz4Block(src, out []byte, limit int) ([]byte, error) {
	for i := 0; i < len(src); {
		tok := src[i]
		i++
		lit := int(tok >> 4)
		if lit == 15 {
			for i < len(src) {
				b := src[i]
				i++
				lit += int(b)
				if b != 255 {
					break
				}
			}
		}
		if i+lit > len(src) {
			return out, fmt.Errorf("lz4: литералы за концом блока")
		}
		out = append(out, src[i:i+lit]...)
		i += lit
		if i >= len(src) || limit >= 0 && len(out) >= limit {
			return out, nil // последняя последовательность — только литералы
		}
		if i+2 > len(src) {
			return out, fmt.Errorf("lz4: смещение за концом блока")
		}
		off := int(binary.LittleEndian.Uint16(src[i:]))
		i += 2
		ml := int(tok&0xF) + 4
		if tok&0xF == 15 {
			for i < len(src) {
				b := src[i]
				i++
				ml += int(b)
				if b != 255 {
					break
				}
			}
		}
		if off == 0 || off > len(out) {
			return out, fmt.Errorf("lz4: ссылка за началом данных")
		}
		from := len(out) - off
		for k := range ml { // ссылка может перекрываться с тем, что дописывается
			out = append(out, out[from+k])
		}
	}
	return out, nil
}

// LZ4Frame упаковывает данные в кадр LZ4 (sized — с размером впереди, как *.cmpbin). Блоки пишутся несжатыми:
// звук в DSP-ADPCM почти не сжимается, а кадр от этого остаётся правильным для любого распаковщика.
func LZ4Frame(data []byte, sized bool) []byte {
	const block = 4 << 20
	var out []byte
	if sized {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	}
	out = append(out, lz4Magic...)
	desc := []byte{0x60, 0x70} // версия 1, блоки независимы; блок до 4 МБ
	out = append(out, desc...)
	out = append(out, byte(xxh32(desc)>>8))
	for len(data) > 0 {
		n := min(len(data), block)
		out = binary.LittleEndian.AppendUint32(out, uint32(n)|0x80000000)
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return binary.LittleEndian.AppendUint32(out, 0)
}

// xxh32 — xxHash32 с нулевым зерном (контрольная сумма заголовка кадра LZ4).
func xxh32(b []byte) uint32 {
	var p1, p2, p3, p4, p5 uint32 = 2654435761, 2246822519, 3266489917, 668265263, 374761393
	rotl := func(x uint32, r uint) uint32 { return x<<r | x>>(32-r) }
	n := uint32(len(b))
	var h uint32
	if len(b) >= 16 {
		v1, v2, v3, v4 := p1+p2, p2, uint32(0), -p1
		round := func(v, x uint32) uint32 { return rotl(v+x*p2, 13) * p1 }
		for ; len(b) >= 16; b = b[16:] {
			le := binary.LittleEndian
			v1, v2, v3, v4 = round(v1, le.Uint32(b)), round(v2, le.Uint32(b[4:])), round(v3, le.Uint32(b[8:])), round(v4, le.Uint32(b[12:]))
		}
		h = rotl(v1, 1) + rotl(v2, 7) + rotl(v3, 12) + rotl(v4, 18)
	} else {
		h = p5
	}
	h += n
	for ; len(b) >= 4; b = b[4:] {
		h = rotl(h+binary.LittleEndian.Uint32(b)*p3, 17) * p4
	}
	for _, c := range b {
		h = rotl(h+uint32(c)*p5, 11) * p1
	}
	h ^= h >> 15
	h *= p2
	h ^= h >> 13
	h *= p3
	h ^= h >> 16
	return h
}

// LZ4Block распаковывает один блок LZ4 без кадра (так сжаты блоки бандлов Unity); size — сколько должно выйти.
func LZ4Block(src []byte, size int) ([]byte, error) {
	out, err := lz4Block(src, make([]byte, 0, size), -1)
	if err != nil {
		return nil, err
	}
	if len(out) != size {
		return nil, fmt.Errorf("lz4: вышло %d байт вместо %d", len(out), size)
	}
	return out, nil
}
