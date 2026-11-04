// Package codec — обёртки сжатия, которые встречаются в играх Switch: Yaz0, zstd и LZ4.
package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

// Wrapper определяет обёртку по первым байтам: "Yaz0", "zstd", "lz4", "lz4s" (LZ4 с размером впереди) или "".
func Wrapper(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("Yaz0")):
		return "Yaz0"
	case bytes.HasPrefix(head, zstdMagic):
		return "zstd"
	}
	return lz4Wrapper(head)
}

// Unwrap снимает Yaz0/zstd (рекурсивно — бывает .zs внутри .szs). Без обёртки возвращает data как есть.
func Unwrap(data []byte) ([]byte, string, error) {
	var wrappers []string
	for i := 0; i < 4; i++ {
		var err error
		switch Wrapper(data) {
		case "Yaz0":
			data, err = Yaz0(data, -1)
			wrappers = append(wrappers, "Yaz0")
		case "zstd":
			data, err = Zstd(data)
			wrappers = append(wrappers, "zstd")
		case "lz4", "lz4s":
			wrappers = append(wrappers, Wrapper(data))
			data, err = LZ4(data, -1)
		default:
			return data, join(wrappers), nil
		}
		if err != nil {
			return nil, join(wrappers), err
		}
	}
	return data, join(wrappers), nil
}

func join(w []string) string {
	s := ""
	for i, x := range w {
		if i > 0 {
			s += "+"
		}
		s += x
	}
	return s
}

// Yaz0 распаковывает данные. limit >= 0 — остановиться после limit байт (для подглядывания сигнатуры).
func Yaz0(buf []byte, limit int) ([]byte, error) {
	if len(buf) < 16 {
		return nil, fmt.Errorf("Yaz0: слишком короткий")
	}
	size := int(binary.BigEndian.Uint32(buf[4:]))
	if limit >= 0 && limit < size {
		size = limit
	}
	out := make([]byte, 0, size)
	src := 16
	for len(out) < size {
		if src >= len(buf) {
			if limit >= 0 {
				return out, nil
			}
			return nil, fmt.Errorf("Yaz0: данные обрываются")
		}
		code := buf[src]
		src++
		for bit := 0; bit < 8 && len(out) < size; bit++ {
			if code&(0x80>>bit) != 0 {
				if src >= len(buf) {
					return out, nil
				}
				out = append(out, buf[src])
				src++
				continue
			}
			if src+1 >= len(buf) {
				return out, nil
			}
			b1, b2 := int(buf[src]), int(buf[src+1])
			src += 2
			dist := ((b1&0x0F)<<8 | b2) + 1
			n := b1 >> 4
			if n == 0 {
				if src >= len(buf) {
					return out, nil
				}
				n = int(buf[src]) + 0x12
				src++
			} else {
				n += 2
			}
			if dist > len(out) {
				return nil, fmt.Errorf("Yaz0: ссылка за пределы данных")
			}
			for j := 0; j < n && len(out) < size; j++ {
				out = append(out, out[len(out)-dist])
			}
		}
	}
	return out, nil
}

var zstdDec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<31))

func Zstd(buf []byte) ([]byte, error) {
	return zstdDec.DecodeAll(buf, nil)
}

// ZstdPeek распаковывает только начало потока.
func ZstdPeek(r io.Reader, n int) ([]byte, error) {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	out := make([]byte, n)
	got, err := io.ReadFull(d, out)
	if got > 0 {
		return out[:got], nil
	}
	return nil, err
}
