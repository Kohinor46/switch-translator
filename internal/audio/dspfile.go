package audio

import (
	"encoding/binary"
	"fmt"
)

// Файл DSP-ADPCM со стандартным заголовком 0x60 байт (как .dsp у Nintendo): игры LEGO
// хранят так реплики (*.adpcm), причём заголовок у них в обратном порядке байт.

const dspHeader = 0x60

// dspOrder — порядок байт заголовка DSP или nil, если это не он.
func dspOrder(b []byte) binary.ByteOrder {
	if len(b) < dspHeader+8 {
		return nil
	}
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		samples, nibbles, rate := bo.Uint32(b), bo.Uint32(b[4:]), bo.Uint32(b[8:])
		if rate < 4000 || rate > 96000 || bo.Uint16(b[0x0E:]) != 0 || samples == 0 {
			continue
		}
		if nibbles < samples || uint64(nibbles) > uint64(samples)*16/14+32 {
			continue
		}
		if int(nibbles/2) > len(b)-dspHeader+16 || uint16(b[dspHeader]) != bo.Uint16(b[0x3E:])&0xFF {
			continue
		}
		return bo
	}
	return nil
}

// IsDSPFile — файл DSP-ADPCM с заголовком.
func IsDSPFile(b []byte) bool { return dspOrder(b) != nil }

func decodeDSPFile(b []byte) (*PCM, error) {
	bo := dspOrder(b)
	if bo == nil {
		return nil, ErrUnsupported
	}
	var d dspInfo
	for i := range 16 {
		d.coefs[i] = int16(bo.Uint16(b[0x1C+i*2:]))
	}
	d.hist1, d.hist2 = int16(bo.Uint16(b[0x40:])), int16(bo.Uint16(b[0x42:]))
	samples := int(bo.Uint32(b))
	data := b[dspHeader:]
	if r := len(data) % 8; r != 0 { // последний кадр бывает обрезан по числу полубайтов
		data = append(append([]byte(nil), data...), make([]byte, 8-r)...)
	}
	ch := decodeDSP(data, samples, &d, make([]int16, 0, samples))
	return &PCM{Rate: int(bo.Uint32(b[8:])), Channels: [][]int16{ch}}, nil
}

// ReplaceDSPFile — файл DSP с новым звуком: частота и предсказатели оригинала, без цикла.
func ReplaceDSPFile(orig []byte, p *PCM) ([]byte, error) {
	bo := dspOrder(orig)
	if bo == nil {
		return nil, fmt.Errorf("не файл DSP-ADPCM")
	}
	src := fitChannels(Resample(p, int(bo.Uint32(orig[8:]))), 1)[0]
	if len(src) == 0 {
		return nil, fmt.Errorf("нечего записывать")
	}
	var coefs [16]int16
	for i := range 16 {
		coefs[i] = int16(bo.Uint16(orig[0x1C+i*2:]))
	}
	enc := encodeDSP(src, coefs)
	out := append([]byte(nil), orig[:dspHeader]...)
	n := len(src)
	nibbles := n / 14 * 16 // в каждом кадре 2 полубайта заголовка и 14 сэмплов
	if r := n % 14; r != 0 {
		nibbles += r + 2
	}
	bo.PutUint32(out, uint32(n))
	bo.PutUint32(out[4:], uint32(nibbles))
	bo.PutUint16(out[0x0C:], 0)                 // без цикла
	bo.PutUint32(out[0x10:], 2)                 // начало цикла
	bo.PutUint32(out[0x14:], uint32(nibbles-1)) // конец цикла
	bo.PutUint32(out[0x18:], 2)
	bo.PutUint16(out[0x3E:], uint16(enc[0]))
	bo.PutUint32(out[0x40:], 0) // история
	bo.PutUint16(out[0x44:], uint16(enc[0]))
	bo.PutUint32(out[0x46:], 0)
	return append(out, enc...), nil
}
