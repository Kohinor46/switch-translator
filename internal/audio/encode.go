package audio

import (
	"fmt"
	"hash/crc32"
)

// Replace собирает файл того же вида, что orig (BFSTM, BWAV или BFWAV), но со звуком p.
// BFSTM пишется несжатым PCM 16 бит (игра читает кодек из заголовка, файл просто больше),
// BWAV — сжатым DSP-ADPCM, как у Nintendo. Число каналов подгоняется под оригинал — игра ждёт столько же.
func Replace(orig []byte, p *PCM) ([]byte, error) {
	if len(orig) < 0x20 || p.Samples() == 0 {
		return nil, fmt.Errorf("нечего записывать")
	}
	switch string(orig[:4]) {
	case "FSTM":
		return replaceStream(orig, p)
	case "BWAV":
		return replaceBWAV(orig, p)
	case "FWAV": // BFWAV в старом BARS (Mario Kart 8 Deluxe) — на место оригинала
		out, _, err := ReplaceFWAVInPlace(orig, p)
		return out, err
	}
	return nil, fmt.Errorf("записывать звук в %q программа пока не умеет", string(orig[:4]))
}

// fitChannels делает n каналов: моно копируется, лишние сводятся.
func fitChannels(p *PCM, n int) [][]int16 {
	if len(p.Channels) == n {
		return p.Channels
	}
	out := make([][]int16, n)
	for c := range out {
		if c < len(p.Channels) {
			out[c] = p.Channels[c]
		} else {
			out[c] = p.Channels[len(p.Channels)-1]
		}
	}
	return out
}

func align(n, a int) int { return (n + a - 1) / a * a }

func replaceStream(orig []byte, p *PCM) ([]byte, error) {
	bo := order(orig[4:6])
	r := reader{orig, bo}
	bl := blocks(r)
	info, ok := bl[0x4000]
	if !ok {
		return nil, fmt.Errorf("BFSTM: нет INFO")
	}
	infoSize := r.u32(info + 4)
	base := info + 8
	si := base + r.s32(base+4)
	chans := r.u8(si + 2)
	channels := fitChannels(p, chans)
	n := len(channels[0])

	const blockSize = 0x2000
	const blockSamples = blockSize / 2
	blocksN := (n + blockSamples - 1) / blockSamples
	lastSamples := n - (blocksN-1)*blockSamples
	lastSize := lastSamples * 2
	lastPadded := align(lastSize, 0x20)

	// INFO берём из оригинала и меняем поля потока
	inf := append([]byte(nil), orig[info:info+infoSize]...)
	ir := si - info
	put32 := func(o, v int) { bo.PutUint32(inf[o:], uint32(v)) }
	inf[ir] = codecPCM16
	inf[ir+1] = 0
	loopStart, loopEnd := 0, n
	if p.Loop && p.LoopEnd > p.LoopStart && p.LoopEnd <= n {
		inf[ir+1] = 1
		loopStart, loopEnd = p.LoopStart, p.LoopEnd
	}
	put32(ir+4, p.Rate)
	put32(ir+8, loopStart)
	put32(ir+12, loopEnd)
	put32(ir+16, blocksN)
	put32(ir+20, blockSize)
	put32(ir+24, blockSamples)
	put32(ir+28, lastSize)
	put32(ir+32, lastSamples)
	put32(ir+36, lastPadded)
	put32(ir+40, 0) // таблица перемотки нужна только для DSP-ADPCM
	put32(ir+44, blockSamples)
	inf[ir+3] = 0                                      // без регионов: секция REGN не переносится
	if r.u32(8) >= 0x00050000 && ir+0x4C <= len(inf) { // «исходные» границы цикла
		put32(ir+0x44, loopStart)
		put32(ir+0x48, loopEnd)
	}
	// ссылки каналов на коэффициенты DSP больше не нужны
	ct := base - info + r.s32(base+0x14)
	for i := range r.u32(info + ct) {
		ci := ct + int(int32(bo.Uint32(inf[ct+4+i*8+4:])))
		bo.PutUint16(inf[ci:], 0)
		bo.PutUint32(inf[ci+4:], 0xFFFFFFFF)
	}

	// DATA: заголовок, отступ до 0x20, блоки по каналам
	dataBody := (blocksN-1)*blockSize*chans + lastPadded*chans
	data := make([]byte, 0x20+dataBody)
	copy(data, "DATA")
	bo.PutUint32(data[4:], uint32(len(data)))
	o := 0x20
	for blk := range blocksN {
		cnt, size := blockSamples, blockSize
		if blk == blocksN-1 {
			cnt, size = lastSamples, lastPadded
		}
		for c := range chans {
			src := channels[c][blk*blockSamples : blk*blockSamples+cnt]
			for i, s := range src {
				bo.PutUint16(data[o+i*2:], uint16(s))
			}
			o += size
		}
	}
	// ссылка на начало сэмплов внутри DATA
	bo.PutUint32(inf[ir+0x34:], 0x18)

	// заголовок: две секции — INFO и DATA
	const hdr = 0x40
	out := make([]byte, hdr, hdr+len(inf)+len(data))
	copy(out, orig[:0x10])
	bo.PutUint16(out[6:], hdr)
	bo.PutUint16(out[0x10:], 2)
	infoOff := hdr
	dataOff := align(infoOff+len(inf), 0x20)
	sec := func(i, id, off, size int) {
		b := out[0x14+i*12:]
		bo.PutUint16(b, uint16(id))
		bo.PutUint32(b[4:], uint32(off))
		bo.PutUint32(b[8:], uint32(size))
	}
	sec(0, 0x4000, infoOff, len(inf))
	sec(1, 0x4002, dataOff, len(data))
	out = append(out, inf...)
	out = append(out, make([]byte, dataOff-len(out))...)
	out = append(out, data...)
	bo.PutUint32(out[0x0C:], uint32(len(out)))
	return out, nil
}

// replaceBWAV пишет звук так же, как Nintendo: DSP-ADPCM на частоте оригинала, с коэффициентами
// оригинала (они подобраны под голос) и контрольной суммой данных в заголовке. Несжатый PCM
// игра тоже понимает, но он в 3,5 раза больше — Ring Fit с такими голосовыми архивами падала.
func replaceBWAV(orig []byte, p *PCM) ([]byte, error) {
	bo := order(orig[4:6])
	chans := int(bo.Uint16(orig[0x0E:]))
	if chans == 0 || len(orig) < 0x10+chans*0x4C {
		return nil, fmt.Errorf("BWAV: неожиданный заголовок")
	}
	if rate := int(bo.Uint32(orig[0x14:])); rate > 0 {
		p = Resample(p, rate)
	}
	channels := fitChannels(p, chans)
	n := len(channels[0])
	hdr := align(0x10+chans*0x4C, 0x40)
	nb := dspFrameBytes(n)
	size := align(nb, 0x40)
	out := make([]byte, hdr+size*chans)
	copy(out, orig[:0x10+chans*0x4C])
	bo.PutUint16(out[0x0C:], 0) // без предзагрузки
	loop := p.Loop && p.LoopEnd > p.LoopStart && p.LoopEnd <= n
	for c := range chans {
		h := 0x10 + c*0x4C
		start := hdr + c*size
		coefs := dspDefaultCoefs
		if bo.Uint16(orig[h:]) == 1 {
			for k := range 16 {
				coefs[k] = int16(bo.Uint16(orig[h+0x10+k*2:]))
			}
		}
		data := encodeDSP(channels[c], coefs)
		copy(out[start:], data)
		bo.PutUint16(out[h:], 1) // DSP-ADPCM
		bo.PutUint32(out[h+4:], uint32(p.Rate))
		bo.PutUint32(out[h+8:], uint32(n))
		bo.PutUint32(out[h+0xC:], uint32(n))
		for k, v := range coefs {
			bo.PutUint16(out[h+0x10+k*2:], uint16(v))
		}
		bo.PutUint32(out[h+0x30:], uint32(start))
		bo.PutUint32(out[h+0x34:], uint32(start))
		if loop {
			bo.PutUint32(out[h+0x38:], 1)
			bo.PutUint32(out[h+0x3C:], uint32(p.LoopEnd))
			bo.PutUint32(out[h+0x40:], uint32(p.LoopStart))
		} else if le := bo.Uint32(out[h+0x3C:]); le != 0xFFFFFFFF && int(le) > n {
			// флаг и границы цикла — как у оригинала (у Ring Fit: «до конца» = FFFFFFFF), но не за концом звука
			bo.PutUint32(out[h+0x3C:], uint32(n))
			if int(bo.Uint32(out[h+0x40:])) >= n {
				bo.PutUint32(out[h+0x40:], 0)
			}
		}
		bo.PutUint16(out[h+0x44:], uint16(data[0])) // первый заголовок кадра
		clear(out[h+0x46 : h+0x4C])                 // история — с нуля
	}
	// CRC32 сжатых данных (у моно-звуков Ring Fit — ровно по первому каналу)
	bo.PutUint32(out[8:], crc32.ChecksumIEEE(out[hdr:hdr+nb]))
	return out, nil
}
