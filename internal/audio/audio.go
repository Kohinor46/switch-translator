// Package audio читает и пишет звук Nintendo (BFSTM, BFWAV, BWAV) без внешних программ.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// PCM — расшифрованный звук: 16 бит, каналы отдельно.
type PCM struct {
	Rate      int
	Channels  [][]int16
	Loop      bool
	LoopStart int // в сэмплах
	LoopEnd   int
}

func (p *PCM) Samples() int {
	if len(p.Channels) == 0 {
		return 0
	}
	return len(p.Channels[0])
}

func (p *PCM) Seconds() float64 {
	if p.Rate == 0 {
		return 0
	}
	return float64(p.Samples()) / float64(p.Rate)
}

// Codec — кодеки внутри файлов Nintendo.
const (
	codecPCM8  = 0
	codecPCM16 = 1
	codecDSP   = 2
)

var ErrUnsupported = errors.New("этот формат звука программа пока не умеет читать сама")

// Supported — можно ли расшифровать файл своим кодом (по первым байтам).
func Supported(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch string(head[:4]) {
	case "FSTM", "FWAV", "BWAV", "CSTM", "CWAV", "BARS":
		return true
	case "FSB5":
		return fsbSupported(head)
	}
	return IsDSPFile(head)
}

// Decode расшифровывает BFSTM/BCSTM, BFWAV/BCWAV, BWAV или FSB5 (PCM, FADPCM).
func Decode(b []byte) (p *PCM, err error) {
	if len(b) < 0x20 {
		return nil, ErrUnsupported
	}
	// на битом или необычном файле разбор не должен ронять программу
	defer func() {
		if r := recover(); r != nil {
			p, err = nil, fmt.Errorf("не удалось разобрать звук: %v", r)
		}
	}()
	switch string(b[:4]) {
	case "FSTM", "CSTM":
		p, err = decodeStream(b)
	case "FWAV", "CWAV":
		p, err = decodeWave(b)
	case "BWAV":
		p, err = decodeBWAV(b)
	case "BARS":
		p, err = decodeBARS(b)
	case "FSB5":
		p, err = decodeFSB(b)
	default:
		if !IsDSPFile(b) {
			return nil, ErrUnsupported
		}
		p, err = decodeDSPFile(b)
	}
	if p != nil {
		p.evenChannels()
	}
	return p, err
}

// evenChannels дополняет тишиной каналы, которые короче самого длинного:
// в BWAV у каналов бывает разное число сэмплов (так устроены некоторые голоса Ring Fit).
func (p *PCM) evenChannels() {
	n := 0
	for _, c := range p.Channels {
		n = max(n, len(c))
	}
	for i, c := range p.Channels {
		if len(c) < n {
			p.Channels[i] = append(c, make([]int16, n-len(c))...)
		}
	}
}

// decodeBARS — весь архив одним звуком (для архивов, где все звуки — начала музыки):
// звуки склеиваются подряд с паузой в секунду.
func decodeBARS(b []byte) (*PCM, error) {
	a, err := ParseBARS(b)
	if err != nil {
		return nil, err
	}
	var out *PCM
	for _, k := range a.order {
		p, err := Decode(a.Entries[k].BWAV)
		if err != nil {
			return nil, err
		}
		p.evenChannels()
		if out == nil {
			out = &PCM{Rate: p.Rate, Channels: make([][]int16, len(p.Channels))}
		} else {
			p = Resample(p, out.Rate)
			for c := range out.Channels {
				out.Channels[c] = append(out.Channels[c], make([]int16, out.Rate)...)
			}
		}
		for c := range out.Channels {
			out.Channels[c] = append(out.Channels[c], p.Channels[min(c, len(p.Channels)-1)]...)
		}
	}
	return out, nil
}

// reader — чтение с нужным порядком байт и проверкой границ.
type reader struct {
	b  []byte
	bo binary.ByteOrder
}

func (r reader) u8(o int) int    { return int(r.b[o]) }
func (r reader) u16(o int) int   { return int(r.bo.Uint16(r.b[o:])) }
func (r reader) s16(o int) int16 { return int16(r.bo.Uint16(r.b[o:])) }
func (r reader) u32(o int) int   { return int(r.bo.Uint32(r.b[o:])) }
func (r reader) s32(o int) int   { return int(int32(r.bo.Uint32(r.b[o:]))) }

func order(bom []byte) binary.ByteOrder {
	if bom[0] == 0xFE && bom[1] == 0xFF {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// blocks находит секции файла по их id (0x4000 INFO, 0x4002 DATA…).
func blocks(r reader) map[int]int {
	n := r.u16(0x10)
	m := map[int]int{}
	for i := range n {
		o := 0x14 + i*12
		if o+12 > len(r.b) {
			break
		}
		m[r.u16(o)] = r.u32(o + 4)
	}
	return m
}

// dspInfo — коэффициенты DSP-ADPCM канала и начальная история.
type dspInfo struct {
	coefs        [16]int16
	hist1, hist2 int16
}

func readDSP(r reader, o int) dspInfo {
	var d dspInfo
	for i := range 16 {
		d.coefs[i] = r.s16(o + i*2)
	}
	d.hist1 = r.s16(o + 0x22)
	d.hist2 = r.s16(o + 0x24)
	return d
}

// decodeStream — BFSTM: данные чередуются блоками по каналам.
func decodeStream(b []byte) (*PCM, error) {
	r := reader{b, order(b[4:6])}
	bl := blocks(r)
	info, okI := bl[0x4000]
	data, okD := bl[0x4002]
	if !okI || !okD || info+0x20 > len(b) {
		return nil, fmt.Errorf("BFSTM: нет секций INFO/DATA")
	}
	base := info + 8
	si := base + r.s32(base+4)
	codec, loop, chans := r.u8(si), r.u8(si+1) != 0, r.u8(si+2)
	p := &PCM{Rate: r.u32(si + 4), Loop: loop, LoopStart: r.u32(si + 8), LoopEnd: r.u32(si + 12)}
	total := r.u32(si + 12)
	blockCount, blockSize, blockSamples := r.u32(si+16), r.u32(si+20), r.u32(si+24)
	lastSize, lastSamples, lastPadded := r.u32(si+28), r.u32(si+32), r.u32(si+36)
	_ = lastSize
	dataStart := data + 8 + r.s32(si+0x34)
	if chans == 0 || chans > 16 || blockCount == 0 {
		return nil, fmt.Errorf("BFSTM: странные параметры потока")
	}

	var dsp []dspInfo
	if codec == codecDSP {
		ct := base + r.s32(base+0x14) // таблица каналов
		n := r.u32(ct)
		for i := range min(n, chans) {
			ci := ct + r.s32(ct+4+i*8+4)
			dsp = append(dsp, readDSP(r, ci+r.s32(ci+4)))
		}
		if len(dsp) < chans {
			return nil, fmt.Errorf("BFSTM: нет коэффициентов DSP")
		}
	}
	p.Channels = make([][]int16, chans)
	for c := range p.Channels {
		p.Channels[c] = make([]int16, 0, total)
	}
	off := dataStart
	for blk := range blockCount {
		size, samples := blockSize, blockSamples
		if blk == blockCount-1 {
			size, samples = lastPadded, lastSamples
		}
		for c := range chans {
			if off+size > len(b) {
				return nil, fmt.Errorf("BFSTM: файл обрезан")
			}
			chunk := b[off : off+size]
			switch codec {
			case codecDSP:
				p.Channels[c] = decodeDSP(chunk, samples, &dsp[c], p.Channels[c])
			case codecPCM16:
				p.Channels[c] = appendPCM16(p.Channels[c], chunk, samples, r.bo)
			case codecPCM8:
				p.Channels[c] = appendPCM8(p.Channels[c], chunk, samples)
			default:
				return nil, ErrUnsupported
			}
			off += size
		}
	}
	return p, nil
}

// decodeWave — BFWAV: каждый канал лежит целиком.
func decodeWave(b []byte) (*PCM, error) {
	r := reader{b, order(b[4:6])}
	bl := blocks(r)
	info, okI := bl[0x7000]
	data, okD := bl[0x7001]
	if !okI || !okD {
		return nil, fmt.Errorf("BFWAV: нет секций INFO/DATA")
	}
	base := info + 8
	codec, loop := r.u8(base), r.u8(base+1) != 0
	p := &PCM{Rate: r.u32(base + 4), Loop: loop, LoopStart: r.u32(base + 8), LoopEnd: r.u32(base + 12)}
	total := p.LoopEnd
	ct := base + 0x14 // версии 1.1+: после начала цикла идёт «исходное начало цикла»
	if r.u32(8) < 0x00010100 {
		ct = base + 0x10
	}
	n := r.u32(ct)
	for i := range n {
		ci := ct + r.s32(ct+4+i*8+4)
		start := data + 8 + r.s32(ci+4)
		var ch []int16
		switch codec {
		case codecDSP:
			d := readDSP(r, ci+r.s32(ci+0xC))
			nb := (total + 13) / 14 * 8
			if start+nb > len(b) {
				nb = len(b) - start
			}
			ch = decodeDSP(b[start:start+nb], total, &d, nil)
		case codecPCM16:
			ch = appendPCM16(nil, b[start:min(len(b), start+total*2)], total, r.bo)
		case codecPCM8:
			ch = appendPCM8(nil, b[start:min(len(b), start+total)], total)
		default:
			return nil, ErrUnsupported
		}
		p.Channels = append(p.Channels, ch)
	}
	return p, nil
}

// decodeBWAV — формат новых игр Nintendo: заголовок 0x10 и по 0x4C на канал.
func decodeBWAV(b []byte) (*PCM, error) {
	r := reader{b, order(b[4:6])}
	n := r.u16(0x0E)
	p := &PCM{}
	for i := range n {
		h := 0x10 + i*0x4C
		if h+0x4C > len(b) {
			return nil, fmt.Errorf("BWAV: файл обрезан")
		}
		codec := r.u16(h)
		p.Rate = r.u32(h + 4)
		total := r.u32(h + 0xC)
		start := r.u32(h + 0x34)
		p.Loop = r.u32(h+0x38) != 0
		p.LoopEnd, p.LoopStart = r.u32(h+0x3C), r.u32(h+0x40)
		var ch []int16
		switch codec {
		case 1:
			var d dspInfo
			for k := range 16 {
				d.coefs[k] = r.s16(h + 0x10 + k*2)
			}
			d.hist1, d.hist2 = r.s16(h+0x46), r.s16(h+0x48)
			nb := min((total+13)/14*8, len(b)-start)
			ch = decodeDSP(b[start:start+nb], total, &d, nil)
		case 0:
			ch = appendPCM16(nil, b[start:min(len(b), start+total*2)], total, r.bo)
		default:
			return nil, ErrUnsupported
		}
		p.Channels = append(p.Channels, ch)
	}
	if n == 0 {
		return nil, fmt.Errorf("BWAV: нет каналов")
	}
	return p, nil
}

// decodeDSP расшифровывает DSP-ADPCM: кадр 8 байт → 14 сэмплов.
func decodeDSP(src []byte, samples int, d *dspInfo, out []int16) []int16 {
	h1, h2 := int32(d.hist1), int32(d.hist2)
	for f := 0; f+8 <= len(src) && samples > 0; f += 8 {
		ps := src[f]
		scale := int32(1) << (ps & 0xF)
		c1, c2 := int32(d.coefs[(ps>>4)*2]), int32(d.coefs[(ps>>4)*2+1])
		for i := 0; i < 14 && samples > 0; i++ {
			nib := int32(src[f+1+i/2])
			if i%2 == 0 {
				nib >>= 4
			} else {
				nib &= 0xF
			}
			if nib >= 8 {
				nib -= 16
			}
			s := ((nib*scale)<<11 + 1024 + c1*h1 + c2*h2) >> 11
			s = max(math.MinInt16, min(math.MaxInt16, s))
			out = append(out, int16(s))
			h2, h1 = h1, s
			samples--
		}
	}
	d.hist1, d.hist2 = int16(h1), int16(h2)
	return out
}

func appendPCM16(out []int16, src []byte, samples int, bo binary.ByteOrder) []int16 {
	for i := 0; i < samples && i*2+2 <= len(src); i++ {
		out = append(out, int16(bo.Uint16(src[i*2:])))
	}
	return out
}

func appendPCM8(out []int16, src []byte, samples int) []int16 {
	for i := 0; i < samples && i < len(src); i++ {
		out = append(out, int16(int8(src[i]))<<8)
	}
	return out
}
