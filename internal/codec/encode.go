package codec

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Wrap упаковывает данные обратно в цепочку обёрток из Unwrap ("Yaz0", "zstd", "Yaz0+zstd"…).
// Если orig не nil, служебные поля заголовка Yaz0 (выравнивание) берутся из него.
func Wrap(data []byte, chain string, orig []byte) ([]byte, error) {
	if chain == "" {
		return data, nil
	}
	w := strings.Split(chain, "+")
	for i := len(w) - 1; i >= 0; i-- {
		switch w[i] {
		case "Yaz0":
			data = Yaz0Encode(data)
			if i == 0 && len(orig) >= 16 && Wrapper(orig) == "Yaz0" {
				copy(data[8:16], orig[8:16])
			}
		case "zstd":
			data = zstdEnc.EncodeAll(data, nil)
		case "lz4", "lz4s":
			data = LZ4Frame(data, w[i] == "lz4s")
		default:
			return nil, fmt.Errorf("сжимать в %s программа пока не умеет", w[i])
		}
	}
	return data, nil
}

var zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))

// Yaz0Encode сжимает данные в Yaz0: жадный поиск совпадений по хеш-цепочкам в окне 4 КБ.
func Yaz0Encode(src []byte) []byte {
	out := make([]byte, 16, 16+len(src)+len(src)/8+16)
	copy(out, "Yaz0")
	binary.BigEndian.PutUint32(out[4:], uint32(len(src)))

	const (
		window   = 0x1000
		maxLen   = 0x111
		hashBits = 15
		maxChain = 64
	)
	head := make([]int32, 1<<hashBits)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, len(src))
	hash := func(i int) uint32 {
		return (uint32(src[i])<<16 | uint32(src[i+1])<<8 | uint32(src[i+2])) * 2654435761 >> (32 - hashBits)
	}
	insert := func(i int) {
		if i+3 <= len(src) {
			h := hash(i)
			prev[i] = head[h]
			head[h] = int32(i)
		}
	}
	find := func(i int) (dist, length int) {
		if i+3 > len(src) {
			return 0, 0
		}
		limit := min(maxLen, len(src)-i)
		for j, n := head[hash(i)], 0; j >= 0 && i-int(j) <= window && n < maxChain; j, n = prev[j], n+1 {
			k := 0
			for k < limit && src[int(j)+k] == src[i+k] {
				k++
			}
			if k > length {
				dist, length = i-int(j), k
				if k == limit {
					break
				}
			}
		}
		return dist, length
	}

	codePos, bit := 0, 8
	for i := 0; i < len(src); {
		if bit == 8 {
			codePos = len(out)
			out = append(out, 0)
			bit = 0
		}
		dist, n := find(i)
		if n >= 3 {
			// ленивое сравнение: если со следующего байта совпадение длиннее, пишем литерал
			insert(i)
			if _, n2 := find(i + 1); n2 > n+1 {
				out[codePos] |= 0x80 >> bit
				out = append(out, src[i])
				i++
				bit++
				continue
			}
			d := dist - 1
			if n < 0x12 {
				out = append(out, byte((n-2)<<4|d>>8), byte(d))
			} else {
				out = append(out, byte(d>>8), byte(d), byte(n-0x12))
			}
			for k := 1; k < n; k++ {
				insert(i + k)
			}
			i += n
		} else {
			insert(i)
			out[codePos] |= 0x80 >> bit
			out = append(out, src[i])
			i++
		}
		bit++
	}
	return out
}
