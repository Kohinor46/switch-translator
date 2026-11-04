package audio

import (
	"encoding/binary"
	"fmt"
	"math"
)

// WAV собирает файл WAV (PCM 16 бит) — его играет любой плеер.
func WAV(p *PCM) []byte {
	n, ch := p.Samples(), len(p.Channels)
	data := n * ch * 2
	b := make([]byte, 44+data)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(ch))
	binary.LittleEndian.PutUint32(b[24:], uint32(p.Rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(p.Rate*ch*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(ch*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	o := 44
	for i := range n {
		for c := range ch {
			binary.LittleEndian.PutUint16(b[o:], uint16(p.Channels[c][i]))
			o += 2
		}
	}
	return b
}

// ReadWAV читает WAV: PCM 8/16/24/32 бит или float32.
func ReadWAV(b []byte) (*PCM, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("это не WAV")
	}
	var format, chans, bits, rate int
	for o := 12; o+8 <= len(b); {
		id, size := string(b[o:o+4]), int(binary.LittleEndian.Uint32(b[o+4:]))
		body := b[o+8 : min(len(b), o+8+size)]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, fmt.Errorf("WAV: битый заголовок")
			}
			format = int(binary.LittleEndian.Uint16(body))
			chans = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
			if format == 0xFFFE && len(body) >= 26 { // WAVE_FORMAT_EXTENSIBLE
				format = int(binary.LittleEndian.Uint16(body[24:]))
			}
		case "data":
			if chans == 0 || bits == 0 {
				return nil, fmt.Errorf("WAV: нет заголовка fmt")
			}
			if size == 0 || size > len(b)-o-8 { // поток без размера
				body = b[o+8:]
			}
			return readFrames(body, format, chans, bits, rate)
		}
		o += 8 + size + size%2
	}
	return nil, fmt.Errorf("WAV: нет данных")
}

func readFrames(d []byte, format, chans, bits, rate int) (*PCM, error) {
	bps := bits / 8
	if bps == 0 || (format != 1 && format != 3) {
		return nil, fmt.Errorf("WAV: формат %d/%d бит не поддерживается", format, bits)
	}
	n := len(d) / (bps * chans)
	p := &PCM{Rate: rate, Channels: make([][]int16, chans)}
	for c := range p.Channels {
		p.Channels[c] = make([]int16, n)
	}
	for i := range n {
		for c := range chans {
			s := d[(i*chans+c)*bps:]
			var v int16
			switch {
			case format == 3 && bits == 32:
				f := math.Float32frombits(binary.LittleEndian.Uint32(s))
				v = int16(max(-1, min(1, f)) * 32767)
			case bits == 8:
				v = int16(int(s[0])-128) << 8
			case bits == 16:
				v = int16(binary.LittleEndian.Uint16(s))
			case bits == 24:
				v = int16(uint16(s[1]) | uint16(s[2])<<8)
			case bits == 32:
				v = int16(binary.LittleEndian.Uint32(s) >> 16)
			}
			p.Channels[c][i] = v
		}
	}
	return p, nil
}

// Mono16k — моно 16 кГц float32 для распознавания речи.
func Mono16k(p *PCM) []float32 {
	n := p.Samples()
	if n == 0 || p.Rate == 0 {
		return nil
	}
	mono := make([]float32, n)
	for _, ch := range p.Channels {
		for i, s := range ch {
			mono[i] += float32(s) / 32768 / float32(len(p.Channels))
		}
	}
	return resample(mono, p.Rate, 16000)
}

// Resample меняет частоту каждого канала (линейная интерполяция с фильтром против наложений).
func Resample(p *PCM, rate int) *PCM {
	if p.Rate == rate || rate <= 0 {
		return p
	}
	out := &PCM{Rate: rate, Loop: p.Loop,
		LoopStart: int(int64(p.LoopStart) * int64(rate) / int64(p.Rate)),
		LoopEnd:   int(int64(p.LoopEnd) * int64(rate) / int64(p.Rate))}
	for _, ch := range p.Channels {
		f := make([]float32, len(ch))
		for i, s := range ch {
			f[i] = float32(s)
		}
		r := resample(f, p.Rate, rate)
		o := make([]int16, len(r))
		for i, v := range r {
			o[i] = int16(max(math.MinInt16, min(math.MaxInt16, v)))
		}
		out.Channels = append(out.Channels, o)
	}
	return out
}

func resample(in []float32, from, to int) []float32 {
	if from == to {
		return in
	}
	if to < from { // перед понижением частоты срезаем верх простым скользящим средним
		w := (from + to - 1) / to
		if w > 1 {
			sm := make([]float32, len(in))
			var acc float32
			for i := range in {
				acc += in[i]
				if i >= w {
					acc -= in[i-w]
				}
				sm[i] = acc / float32(min(i+1, w))
			}
			in = sm
		}
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]float32, n)
	step := float64(from) / float64(to)
	for i := range out {
		x := float64(i) * step
		j := int(x)
		fr := float32(x - float64(j))
		a := in[min(j, len(in)-1)]
		b := in[min(j+1, len(in)-1)]
		out[i] = a + (b-a)*fr
	}
	return out
}
