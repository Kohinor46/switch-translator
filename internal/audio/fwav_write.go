package audio

import (
	"fmt"
	"sort"
)

// Запись в BFWAV на месте: звук внутри BFSAR, группы BFGRP или старого BARS переписывается в те же байты,
// поэтому смещения и размеры во всех таблицах вокруг (файлы BFSAR, волновые архивы, группы) остаются верными.
// Кодек и коэффициенты DSP-ADPCM — оригинала. Не помещается на частоте оригинала — частота понижается
// (BFWAV сам хранит свою частоту), но не больше чем вдвое.

// MinFWAVRate — во сколько раз можно понизить частоту, чтобы звук уместился на место оригинала.
const MinFWAVRate = 0.5

// ReplaceFWAVInPlace — BFWAV той же длины, что orig, со звуком p; rate — с какой частотой он записан.
func ReplaceFWAVInPlace(orig []byte, p *PCM) (out []byte, rate int, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, rate, err = nil, 0, fmt.Errorf("BFWAV: не удалось разобрать: %v", r)
		}
	}()
	if len(orig) < 0x40 || string(orig[:4]) != "FWAV" || p.Samples() == 0 {
		return nil, 0, fmt.Errorf("нечего записывать")
	}
	r := reader{orig, order(orig[4:6])}
	bl := blocks(r)
	info, okI := bl[0x7000]
	data, okD := bl[0x7001]
	if !okI || !okD {
		return nil, 0, fmt.Errorf("BFWAV: нет секций INFO/DATA")
	}
	base := info + 8
	codec := r.u8(base)
	ct := base + 0x14
	if r.u32(8) < 0x00010100 {
		ct = base + 0x10
	}
	chans := r.u32(ct)
	if chans == 0 || chans > 8 {
		return nil, 0, fmt.Errorf("BFWAV: %d каналов", chans)
	}
	dataEnd := min(len(orig), data+r.u32(data+4))
	type channel struct{ info, start, room int }
	cs := make([]channel, chans)
	for i := range cs {
		ci := ct + r.s32(ct+4+i*8+4)
		cs[i] = channel{info: ci, start: data + 8 + r.s32(ci+4)}
	}
	// место канала — до начала следующего по порядку в файле или до конца DATA
	order := make([]int, chans)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return cs[order[a]].start < cs[order[b]].start })
	room := len(orig)
	for k, i := range order {
		end := dataEnd
		if k+1 < len(order) {
			end = cs[order[k+1]].start
		}
		cs[i].room = end - cs[i].start
		room = min(room, cs[i].room)
	}
	var fit int // сколько сэмплов помещается в место канала
	switch codec {
	case codecDSP:
		fit = room / 8 * 14
	case codecPCM16:
		fit = room / 2
	default:
		return nil, 0, fmt.Errorf("записывать BFWAV с кодеком %d программа пока не умеет", codec)
	}
	orate := r.u32(base + 4)
	rate = orate
	src := Resample(p, rate)
	if need := src.Samples(); need > fit {
		rate = int(int64(orate) * int64(fit) / int64(need))
		if float64(rate) < float64(orate)*MinFWAVRate {
			return nil, 0, fmt.Errorf("озвучка в %.1f раза длиннее места оригинала — сократи перевод", float64(need)/float64(fit))
		}
		src = Resample(p, rate)
	}
	channels := fitChannels(src, chans)
	n := min(len(channels[0]), fit)

	out = append([]byte(nil), orig...)
	bo := r.bo
	out[base+1] = 0 // без цикла: это реплика
	bo.PutUint32(out[base+4:], uint32(rate))
	bo.PutUint32(out[base+8:], 0)
	bo.PutUint32(out[base+12:], uint32(n))
	if ct == base+0x14 {
		bo.PutUint32(out[base+0x10:], 0)
	}
	for i, c := range cs {
		clear(out[c.start : c.start+c.room])
		ch := channels[i][:n]
		switch codec {
		case codecDSP:
			o := c.info + r.s32(c.info+0xC)
			var coefs [16]int16
			for k := range coefs {
				coefs[k] = r.s16(o + 2*k)
			}
			enc := encodeDSP(ch, coefs)
			copy(out[c.start:], enc)
			bo.PutUint16(out[o+0x20:], uint16(enc[0])) // заголовок первого кадра
			clear(out[o+0x22 : o+0x26])                // история — с нуля
			bo.PutUint16(out[o+0x26:], uint16(enc[0])) // то же для цикла
			clear(out[o+0x28 : o+0x2C])
		case codecPCM16:
			for k, v := range ch {
				bo.PutUint16(out[c.start+2*k:], uint16(v))
			}
		}
	}
	return out, rate, nil
}

// ReplaceWave вписывает звук p на место BFWAV w в data (BFSAR или группа BFGRP целиком) и говорит, с какой частотой.
func ReplaceWave(data []byte, w SARWave, p *PCM) (rate int, err error) {
	if w.off < 0 || w.off+w.size > int64(len(data)) {
		return 0, fmt.Errorf("звук за концом архива")
	}
	nb, rate, err := ReplaceFWAVInPlace(data[w.off:w.off+w.size], p)
	if err != nil {
		return 0, err
	}
	copy(data[w.off:], nb)
	return rate, nil
}

// ReplaceWavesInPlace — ReplaceWave для нескольких звуков; rates — с какой частотой записан каждый.
func ReplaceWavesInPlace(data []byte, waves []SARWave, sounds []*PCM) (rates []int, err error) {
	for i, w := range waves {
		rate, err := ReplaceWave(data, w, sounds[i])
		if err != nil {
			return nil, err
		}
		rates = append(rates, rate)
	}
	return rates, nil
}

// OrigRate — частота BFWAV (0 — не BFWAV).
func OrigRate(fwav []byte) int {
	if len(fwav) < 0x40 || string(fwav[:4]) != "FWAV" {
		return 0
	}
	r := reader{fwav, order(fwav[4:6])}
	info, ok := blocks(r)[0x7000]
	if !ok {
		return 0
	}
	return r.u32(info + 12)
}
