package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// FMOD: звук игр на FMOD Studio лежит в банках .bank (RIFF «FEV ») — внутри чанков «SND » банки сэмплов FSB5,
// где тысячи реплик и эффектов подряд. Банк читается кусками (сотни мегабайт целиком не нужны), реплика
// отдаётся отдельным FSB5 с одним сэмплом, а при сборке озвучка вписывается тем же кодеком на месте оригинала.

// FSB5 — кодеки, которые программа читает и пишет сама; остальные (Vorbis, Opus…) читает vgmstream.
const (
	fsbPCM8   = 1
	fsbPCM16  = 2
	fsbFADPCM = 16
)

var fsbRates = [...]int{4000, 8000, 11000, 11025, 16000, 22050, 24000, 32000, 44100, 48000, 96000}

// FSBSample — звук в банке FMOD.
type FSBSample struct {
	Name     string
	Rate     int
	Channels int
	Samples  int
	Codec    int

	fsb       int    // какой банк FSB5 в файле
	hdr       []byte // заголовок сэмпла: 8 байт и дополнительные чанки
	off, size int64  // данные: смещение в файле и размер
}

// Size — сколько байт занимает звук в банке.
func (s FSBSample) Size() int64 { return s.size }

type fsbBlock struct {
	off, size int64 // FSB5 в файле
	base      []byte
	names     []byte
	dataOff   int64 // начало данных в файле
}

// FMODBank — оглавление банка .bank или файла FSB5.
type FMODBank struct {
	Samples []FSBSample
	fsbs    []fsbBlock
	size    int64
	riff    bool
}

// IsFMOD — банк FMOD Studio или FSB5 (по первым байтам).
func IsFMOD(head []byte) bool {
	return len(head) >= 12 && (string(head[:4]) == "FSB5" || string(head[:4]) == "RIFF" && string(head[8:12]) == "FEV ")
}

// ParseFMOD читает оглавление банка: где какие банки FSB5 и сэмплы в них.
func ParseFMOD(r io.ReaderAt, size int64) (*FMODBank, error) {
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	b := &FMODBank{size: size}
	switch {
	case string(head[:4]) == "FSB5":
		// в .resource Unity звуки лежат отдельными FSB5 подряд (с выравниванием между ними)
		for off := int64(0); off >= 0 && off < size; off = nextFSB(r, off, size) {
			if err := b.addFSB(r, off, size-off); err != nil {
				if off == 0 {
					return nil, err
				}
				off += 4 // ложная подпись внутри чужих данных — ищем дальше
				continue
			}
			off += b.fsbs[len(b.fsbs)-1].size
		}
	case string(head[:4]) == "RIFF" && string(head[8:12]) == "FEV ":
		b.riff = true
		err := walkRIFF(r, 12, size, func(id string, off, n int64) error {
			if id != "SND " {
				return nil
			}
			// перед FSB5 — выравнивание до 32 байт от начала файла
			start := off + 8 + (32-(off+8)%32)%32
			if start+4 > off+8+n {
				return nil
			}
			return b.addFSB(r, start, off+8+n-start)
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("это не банк FMOD")
	}
	if len(b.Samples) == 0 {
		return nil, fmt.Errorf("в банке FMOD нет звуков")
	}
	// имена бывают одинаковыми (или их нет вовсе) — звук ищется по имени, поэтому делаем их разными
	seen := map[string]int{}
	for i := range b.Samples {
		s := &b.Samples[i]
		if s.Name == "" {
			s.Name = fmt.Sprintf("sound_%04d", i)
		}
		if n := seen[s.Name]; n > 0 {
			seen[s.Name]++
			s.Name += "_" + strconv.Itoa(n+1)
		} else {
			seen[s.Name] = 1
		}
	}
	return b, nil
}

// nextFSB — где начинается следующий FSB5 от off; −1 — больше нет.
func nextFSB(r io.ReaderAt, off, size int64) int64 {
	buf := make([]byte, 64<<10)
	for ; off < size; off += int64(len(buf)) - 3 {
		n, _ := r.ReadAt(buf, off)
		if n < 4 {
			return -1
		}
		if i := bytes.Index(buf[:n], []byte("FSB5")); i >= 0 {
			return off + int64(i)
		}
		if n < len(buf) {
			return -1
		}
	}
	return -1
}

// walkRIFF обходит чанки RIFF/LIST вглубь, вызывая fn для обычных чанков.
func walkRIFF(r io.ReaderAt, off, end int64, fn func(id string, off, n int64) error) error {
	h := make([]byte, 12)
	for off+8 <= end {
		if _, err := r.ReadAt(h[:8], off); err != nil {
			return err
		}
		id, n := string(h[:4]), int64(binary.LittleEndian.Uint32(h[4:8]))
		if off+8+n > end {
			return fmt.Errorf("банк FMOD: чанк %q выходит за конец файла", id)
		}
		if id == "LIST" || id == "RIFF" {
			if err := walkRIFF(r, off+12, off+8+n, fn); err != nil {
				return err
			}
		} else if err := fn(id, off, n); err != nil {
			return err
		}
		off += 8 + n + n&1
	}
	return nil
}

func (b *FMODBank) addFSB(r io.ReaderAt, off, size int64) error {
	if size < 0x3C {
		return fmt.Errorf("FSB5: слишком короткий")
	}
	base := make([]byte, 0x40)
	if _, err := r.ReadAt(base[:min(int64(len(base)), size)], off); err != nil && err != io.EOF {
		return err
	}
	if string(base[:4]) != "FSB5" {
		return fmt.Errorf("FSB5: нет подписи")
	}
	le := binary.LittleEndian
	version := le.Uint32(base[4:])
	count := int(le.Uint32(base[8:]))
	hdrSize, nameSize, dataSize := int64(le.Uint32(base[12:])), int64(le.Uint32(base[16:])), int64(le.Uint32(base[20:]))
	codec := int(le.Uint32(base[24:]))
	baseLen := int64(0x3C)
	if version == 0 {
		baseLen = 0x40
	}
	if baseLen+hdrSize+nameSize+dataSize > size || count <= 0 {
		return fmt.Errorf("FSB5: размеры не сходятся")
	}
	meta := make([]byte, hdrSize+nameSize)
	if _, err := r.ReadAt(meta, off+baseLen); err != nil {
		return err
	}
	hdrs, names := meta[:hdrSize], meta[hdrSize:]
	blk := fsbBlock{off: off, size: baseLen + hdrSize + nameSize + dataSize, base: base[:baseLen], names: names,
		dataOff: off + baseLen + hdrSize + nameSize}
	first := len(b.Samples)
	o := 0
	for i := range count {
		if o+8 > len(hdrs) {
			return fmt.Errorf("FSB5: заголовки сэмплов обрываются")
		}
		m := le.Uint64(hdrs[o:])
		s := FSBSample{Codec: codec, fsb: len(b.fsbs)}
		s.Samples = int(m >> 34)
		s.off = int64((m>>7)&0x7FFFFFF) << 5
		s.Channels = [...]int{1, 2, 6, 8}[(m>>5)&3]
		if k := int(m>>1) & 0xF; k < len(fsbRates) {
			s.Rate = fsbRates[k]
		}
		end := o + 8
		for more := m&1 != 0; more; {
			if end+4 > len(hdrs) {
				return fmt.Errorf("FSB5: доп. чанки обрываются")
			}
			x := le.Uint32(hdrs[end:])
			n, typ := int(x>>1)&0xFFFFFF, x>>25
			if end+4+n > len(hdrs) {
				return fmt.Errorf("FSB5: доп. чанк выходит за заголовок")
			}
			body := hdrs[end+4 : end+4+n]
			switch {
			case typ == 1 && n >= 1:
				s.Channels = int(body[0])
			case typ == 2 && n >= 4:
				s.Rate = int(le.Uint32(body))
			}
			more = x&1 != 0
			end += 4 + n
		}
		s.hdr = hdrs[o:end]
		o = end
		if nameSize >= int64(4*count) {
			no := int(le.Uint32(names[4*i:]))
			if no < len(names) {
				nm := names[no:]
				if z := bytes.IndexByte(nm, 0); z >= 0 {
					nm = nm[:z]
				}
				s.Name = string(nm)
			}
		}
		b.Samples = append(b.Samples, s)
	}
	// размер данных сэмпла — до начала следующего по смещению
	own := b.Samples[first:]
	order := make([]int, len(own))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, c int) bool { return own[order[a]].off < own[order[c]].off })
	for k, i := range order {
		next := dataSize
		if k+1 < len(order) {
			next = own[order[k+1]].off
		}
		own[i].size = max(0, next-own[i].off)
		own[i].off += blk.dataOff
	}
	b.fsbs = append(b.fsbs, blk)
	return nil
}

// Find — номер звука по имени.
func (b *FMODBank) Find(name string) (int, bool) {
	for i, s := range b.Samples {
		if s.Name == name {
			return i, true
		}
	}
	return 0, false
}

// Sample — звук i отдельным файлом FSB5 с одним сэмплом: его читает и Decode, и vgmstream.
func (b *FMODBank) Sample(r io.ReaderAt, i int) ([]byte, error) {
	s := b.Samples[i]
	data := make([]byte, s.size)
	if _, err := r.ReadAt(data, s.off); err != nil && err != io.EOF {
		return nil, err
	}
	return b.fsb(s.fsb, []FSBSample{s}, [][]byte{data}), nil
}

// fsb собирает FSB5 из сэмплов samples (заголовки — их собственные) с данными data.
func (b *FMODBank) fsb(blk int, samples []FSBSample, data [][]byte) []byte {
	le := binary.LittleEndian
	var hdrs, names, body bytes.Buffer
	nameOffs := make([]byte, 4*len(samples))
	for i, s := range samples {
		off := body.Len()
		h := append([]byte(nil), s.hdr...)
		m := le.Uint64(h)
		m &^= uint64(0x7FFFFFF)<<7 | uint64(0x3FFFFFFF)<<34
		m |= uint64(off>>5)<<7 | uint64(s.Samples)<<34
		le.PutUint64(h, m)
		hdrs.Write(h)
		le.PutUint32(nameOffs[4*i:], uint32(len(nameOffs)+names.Len()))
		names.WriteString(s.Name)
		names.WriteByte(0)
		body.Write(data[i])
		body.Write(make([]byte, align(body.Len(), 32)-body.Len()))
	}
	nameTable := append(nameOffs, names.Bytes()...)
	nameTable = append(nameTable, make([]byte, align(len(nameTable), 16)-len(nameTable))...)
	base := append([]byte(nil), b.fsbs[blk].base...)
	le.PutUint32(base[8:], uint32(len(samples)))
	le.PutUint32(base[12:], uint32(hdrs.Len()))
	le.PutUint32(base[16:], uint32(len(nameTable)))
	le.PutUint32(base[20:], uint32(body.Len()))
	out := append(base, hdrs.Bytes()...)
	out = append(out, nameTable...)
	return append(out, body.Bytes()...)
}

// fsbSupported — FSB5 с одним из кодеков, которые программа читает сама.
func fsbSupported(b []byte) bool {
	if len(b) < 0x1C {
		return false
	}
	switch binary.LittleEndian.Uint32(b[24:]) {
	case fsbPCM8, fsbPCM16, fsbFADPCM:
		return true
	}
	return false
}

// decodeFSB расшифровывает первый сэмпл FSB5 (PCM или FADPCM).
func decodeFSB(b []byte) (*PCM, error) {
	bank, err := ParseFMOD(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	s := bank.Samples[0]
	data := b[s.off : s.off+s.size]
	p := &PCM{Rate: s.Rate, Channels: make([][]int16, s.Channels)}
	switch s.Codec {
	case fsbFADPCM:
		for c := range p.Channels {
			p.Channels[c] = decodeFADPCM(data, c, s.Channels, s.Samples)
		}
	case fsbPCM16:
		for c := range p.Channels {
			ch := make([]int16, 0, s.Samples)
			for i := 0; i < s.Samples && (i*s.Channels+c+1)*2 <= len(data); i++ {
				ch = append(ch, int16(binary.LittleEndian.Uint16(data[(i*s.Channels+c)*2:])))
			}
			p.Channels[c] = ch
		}
	case fsbPCM8:
		for c := range p.Channels {
			ch := make([]int16, 0, s.Samples)
			for i := 0; i < s.Samples && i*s.Channels+c < len(data); i++ {
				ch = append(ch, int16(int8(data[i*s.Channels+c]^0x80))<<8)
			}
			p.Channels[c] = ch
		}
	default:
		return nil, ErrUnsupported
	}
	return p, nil
}

// ReplaceInFMOD собирает банк заново: звуки из repl (по имени) — новым звуком тем же кодеком, частотой
// и числом каналов, остальное — байт в байт. Меняются только банки FSB5 и оглавление их мест (SNDH).
func ReplaceInFMOD(r io.ReaderAt, b *FMODBank, repl map[string]*PCM) ([]byte, error) {
	changed := map[int][]byte{} // новый FSB5 вместо блока
	for blk := range b.fsbs {
		var samples []FSBSample
		var datas [][]byte
		touched := false
		for _, s := range b.Samples {
			if s.fsb != blk {
				continue
			}
			var data []byte
			if p, ok := repl[s.Name]; ok {
				var err error
				if s, data, err = encodeFSBSample(s, p); err != nil {
					return nil, fmt.Errorf("%s: %w", s.Name, err)
				}
				touched = true
			} else {
				data = make([]byte, s.size)
				if _, err := r.ReadAt(data, s.off); err != nil && err != io.EOF {
					return nil, err
				}
			}
			samples = append(samples, s)
			datas = append(datas, data)
		}
		if touched {
			changed[blk] = b.fsb(blk, samples, datas)
		}
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("в банке нет таких звуков")
	}
	if !b.riff {
		return rebuildRaw(r, b, changed)
	}
	return rebuildFEV(r, b, changed)
}

// rebuildRaw — файл из FSB5 подряд: один FSB5 пишется как есть, а в .resource Unity, где на места звуков
// ссылаются ассеты игры, новый FSB5 ложится на место старого и должен в него уместиться.
func rebuildRaw(r io.ReaderAt, b *FMODBank, changed map[int][]byte) ([]byte, error) {
	if len(b.fsbs) == 1 && b.fsbs[0].off == 0 && b.fsbs[0].size == b.size {
		return changed[0], nil
	}
	out := make([]byte, b.size)
	if _, err := r.ReadAt(out, 0); err != nil && err != io.EOF {
		return nil, err
	}
	for blk, fsb := range changed {
		old := b.fsbs[blk]
		if int64(len(fsb)) > old.size {
			return nil, fmt.Errorf("новый звук на %d байт длиннее места старого — сократи перевод", int64(len(fsb))-old.size)
		}
		copy(out[old.off:], fsb)
		clear(out[old.off+int64(len(fsb)) : old.off+old.size])
	}
	return out, nil
}

// rebuildFEV переписывает RIFF банка: чанки SND с новыми FSB5 (снова выровненными до 32 байт),
// размеры RIFF/LIST и SNDH — где теперь лежит каждый FSB5. Прочие чанки не меняются.
func rebuildFEV(r io.ReaderAt, b *FMODBank, changed map[int][]byte) ([]byte, error) {
	le := binary.LittleEndian
	var out bytes.Buffer
	var places [][2]uint32 // начало и размер каждого FSB5
	sndh := -1
	blk := 0
	var copyList func(off, end int64) error
	copyList = func(off, end int64) error {
		h := make([]byte, 12)
		for off+8 <= end {
			if _, err := r.ReadAt(h[:8], off); err != nil {
				return err
			}
			id, n := string(h[:4]), int64(le.Uint32(h[4:8]))
			at := out.Len()
			switch {
			case id == "LIST" || id == "RIFF":
				if _, err := r.ReadAt(h[8:12], off+8); err != nil {
					return err
				}
				out.Write(h[:12])
				if err := copyList(off+12, off+8+n); err != nil {
					return err
				}
				le.PutUint32(out.Bytes()[at+4:], uint32(out.Len()-at-8))
			case id == "SND ":
				out.Write(h[:8])
				pad := (32 - out.Len()%32) % 32
				out.Write(make([]byte, pad))
				fsb, ok := changed[blk]
				if !ok {
					old := b.fsbs[blk]
					fsb = make([]byte, old.size)
					if _, err := r.ReadAt(fsb, old.off); err != nil && err != io.EOF {
						return err
					}
				}
				places = append(places, [2]uint32{uint32(out.Len()), uint32(len(fsb))})
				out.Write(fsb)
				le.PutUint32(out.Bytes()[at+4:], uint32(out.Len()-at-8))
				blk++
			default:
				if id == "SNDH" {
					sndh = at
				}
				chunk := make([]byte, 8+n)
				if _, err := r.ReadAt(chunk, off); err != nil {
					return err
				}
				out.Write(chunk)
			}
			if (out.Len()-at)&1 != 0 {
				out.WriteByte(0)
			}
			off += 8 + n + n&1
		}
		return nil
	}
	if err := copyList(0, b.size); err != nil {
		return nil, err
	}
	if blk != len(b.fsbs) {
		return nil, fmt.Errorf("банк FMOD: FSB5 не на своих местах")
	}
	if sndh >= 0 {
		body := out.Bytes()[sndh+8:]
		n := int(le.Uint32(out.Bytes()[sndh+4:]))
		// SNDH: 4 байта заголовка, затем по 8 байт на FSB5 — смещение от начала файла и размер
		if (n-4)/8 != len(places) {
			return nil, fmt.Errorf("банк FMOD: в SNDH %d мест, а FSB5 — %d", (n-4)/8, len(places))
		}
		for i, p := range places {
			le.PutUint32(body[4+8*i:], p[0])
			le.PutUint32(body[8+8*i:], p[1])
		}
	}
	return out.Bytes(), nil
}

// encodeFSBSample кодирует p под сэмпл s: та же частота и число каналов, тот же кодек.
func encodeFSBSample(s FSBSample, p *PCM) (FSBSample, []byte, error) {
	p = Resample(p, s.Rate)
	chans := fitChannels(p, s.Channels)
	n := len(chans[0])
	if n == 0 {
		return s, nil, fmt.Errorf("пустой звук")
	}
	var data []byte
	switch s.Codec {
	case fsbFADPCM:
		data = encodeFADPCM(chans)
	case fsbPCM16:
		data = make([]byte, 2*n*len(chans))
		for i := range n {
			for c, ch := range chans {
				binary.LittleEndian.PutUint16(data[2*(i*len(chans)+c):], uint16(ch[i]))
			}
		}
	default:
		return s, nil, fmt.Errorf("записывать звук FMOD с кодеком %d программа пока не умеет", s.Codec)
	}
	s.Samples = n
	s.hdr = fixLoop(s.hdr, n)
	return s, data, nil
}

// fixLoop укорачивает петлю (доп. чанк 3) до новой длины звука.
func fixLoop(hdr []byte, n int) []byte {
	le := binary.LittleEndian
	h := append([]byte(nil), hdr...)
	if le.Uint64(h)&1 == 0 {
		return h
	}
	for o := 8; o+4 <= len(h); {
		x := le.Uint32(h[o:])
		size, typ := int(x>>1)&0xFFFFFF, x>>25
		if typ == 3 && size >= 8 && o+12 <= len(h) {
			if int(le.Uint32(h[o+8:])) >= n {
				le.PutUint32(h[o+8:], uint32(n-1))
			}
			if int(le.Uint32(h[o+4:])) >= n {
				le.PutUint32(h[o+4:], 0)
			}
		}
		if x&1 == 0 {
			break
		}
		o += 4 + size
	}
	return h
}

// FADPCM — ADPCM FMOD: кадры по 0x8C байт на 256 сэмплов. Заголовок кадра — 8 наборов (по 32 сэмпла)
// номеров коэффициентов и сдвигов по 4 бита и два прошлых сэмпла; дальше по 4 бита на сэмпл.
// Каналы чередуются покадрово.
const (
	fadpcmFrame   = 0x8C
	fadpcmSamples = 256
)

var fadpcmCoefs = [8][2]int32{{0, 0}, {60, 0}, {122, 60}, {115, 52}, {98, 55}}

func decodeFADPCM(data []byte, ch, chans, samples int) []int16 {
	out := make([]int16, 0, samples)
	le := binary.LittleEndian
	for f := ch * fadpcmFrame; f+fadpcmFrame <= len(data) && len(out) < samples; f += fadpcmFrame * chans {
		fr := data[f : f+fadpcmFrame]
		coefs, shifts := le.Uint32(fr), le.Uint32(fr[4:])
		h1, h2 := int32(int16(le.Uint16(fr[8:]))), int32(int16(le.Uint16(fr[10:])))
		for i := range 8 {
			c := fadpcmCoefs[int(coefs>>(4*i)&0xF)%7]
			shift := 22 - int(shifts>>(4*i)&0xF)
			for j := range 4 {
				nib := le.Uint32(fr[0xC+0x10*i+4*j:])
				for k := range 8 {
					v := (int32(nib>>(4*k)&0xF) << 28) >> shift
					v = clamp16((v - h2*c[1] + h1*c[0]) >> 6)
					if len(out) < samples {
						out = append(out, int16(v))
					}
					h2, h1 = h1, v
				}
			}
		}
	}
	return out
}

func clamp16(v int32) int32 { return max(-32768, min(32767, v)) }

// encodeFADPCM кодирует каналы в FADPCM: для каждых 32 сэмплов перебирает коэффициенты и сдвиги
// и берёт пару с наименьшей ошибкой, предсказывая по уже закодированному — как декодер.
func encodeFADPCM(chans [][]int16) []byte {
	n := len(chans[0])
	frames := (n + fadpcmSamples - 1) / fadpcmSamples
	out := make([]byte, frames*len(chans)*fadpcmFrame)
	le := binary.LittleEndian
	for c, src := range chans {
		var h1, h2 int32
		for f := range frames {
			fr := out[(f*len(chans)+c)*fadpcmFrame:][:fadpcmFrame]
			le.PutUint16(fr[8:], uint16(int16(h1)))
			le.PutUint16(fr[10:], uint16(int16(h2)))
			var coefs, shifts uint32
			for i := range 8 {
				var x [32]int32
				for k := range x {
					if at := f*fadpcmSamples + i*32 + k; at < n {
						x[k] = int32(src[at])
					}
				}
				best, bi, bs := int64(-1), 0, 0
				var bestNibs [32]int32
				var bh1, bh2 int32
				for ci := range 5 {
					for sh := range 16 {
						var nibs [32]int32
						e, a1, a2 := fadpcmTry(&x, fadpcmCoefs[ci], sh, h1, h2, &nibs, best)
						if e >= 0 && (best < 0 || e < best) {
							best, bi, bs, bestNibs, bh1, bh2 = e, ci, sh, nibs, a1, a2
						}
					}
				}
				coefs |= uint32(bi) << (4 * i)
				shifts |= uint32(bs) << (4 * i)
				for j := range 4 {
					var w uint32
					for k := range 8 {
						w |= uint32(bestNibs[j*8+k]&0xF) << (4 * k)
					}
					le.PutUint32(fr[0xC+0x10*i+4*j:], w)
				}
				h1, h2 = bh1, bh2
			}
			le.PutUint32(fr, coefs)
			le.PutUint32(fr[4:], shifts)
		}
	}
	return out
}

// fadpcmTry кодирует 32 сэмпла одной парой коэффициентов и сдвигом; −1 — уже хуже limit.
func fadpcmTry(x *[32]int32, c [2]int32, sh int, h1, h2 int32, nibs *[32]int32, limit int64) (int64, int32, int32) {
	step := int32(1) << (6 + sh) // nib * step / 64 — шаг квантования в сэмплах — 2^sh
	var e int64
	for k := range x {
		p := h1*c[0] - h2*c[1]
		want := x[k]*64 - p
		// округление до ближайшего с учётом сдвига вниз (>> 6) в декодере
		nib := (want + step/2) >> (6 + sh)
		if want < 0 {
			nib = -((-want + step/2) >> (6 + sh))
		}
		nib = max(-8, min(7, nib))
		v := clamp16((nib<<(6+sh) + p) >> 6)
		d := int64(v - x[k])
		e += d * d
		if limit >= 0 && e >= limit {
			return -1, 0, 0
		}
		nibs[k] = nib
		h2, h1 = h1, v
	}
	return e, h1, h2
}

// ReplaceInResource — для данных звука Unity (.resource): FSB5 подряд, у каждого AudioClip свой.
// Изменённый FSB5 собирается заново и может стать длиннее: всё, что за ним, сдвигается.
// Если кодек оригинала программа не пишет (Vorbis), звук записывается в PCM16 — FMOD
// берёт кодек из заголовка FSB5. moved: старое начало блока → новое начало и размер.
func ReplaceInResource(r io.ReaderAt, b *FMODBank, repl map[string]*PCM) ([]byte, map[int64][2]int64, error) {
	if b.riff {
		return nil, nil, fmt.Errorf("это банк FMOD, а не данные Unity")
	}
	le := binary.LittleEndian
	changed := map[int][]byte{}
	for blk := range b.fsbs {
		var samples []FSBSample
		var datas [][]byte
		touched, toPCM := false, false
		for _, s := range b.Samples {
			if s.fsb != blk {
				continue
			}
			samples = append(samples, s)
			if _, ok := repl[s.Name]; ok {
				touched = true
				toPCM = toPCM || s.Codec != fsbPCM16 && s.Codec != fsbFADPCM
			}
		}
		if !touched {
			continue
		}
		if toPCM && len(samples) > 1 {
			return nil, nil, fmt.Errorf("%s: в одном FSB5 несколько звуков с кодеком %d — так записывать программа не умеет", samples[0].Name, samples[0].Codec)
		}
		for i, s := range samples {
			var data []byte
			if p, ok := repl[s.Name]; ok {
				if toPCM {
					s.Codec, s.hdr = fsbPCM16, pcmHeader(s)
				}
				var err error
				if s, data, err = encodeFSBSample(s, p); err != nil {
					return nil, nil, fmt.Errorf("%s: %w", s.Name, err)
				}
			} else {
				data = make([]byte, s.size)
				if _, err := r.ReadAt(data, s.off); err != nil && err != io.EOF {
					return nil, nil, err
				}
			}
			samples[i] = s
			datas = append(datas, data)
		}
		fsb := b.fsb(blk, samples, datas)
		if toPCM {
			le.PutUint32(fsb[24:], fsbPCM16)
		}
		changed[blk] = fsb
	}
	if len(changed) == 0 {
		return nil, nil, fmt.Errorf("нет таких звуков")
	}
	moved := map[int64][2]int64{}
	var out []byte
	prev := int64(0)
	for blk, f := range b.fsbs {
		gap := make([]byte, f.off-prev)
		if _, err := r.ReadAt(gap, prev); err != nil && err != io.EOF {
			return nil, nil, err
		}
		out = append(out, gap...)
		data, ok := changed[blk]
		if !ok {
			data = make([]byte, f.size)
			if _, err := r.ReadAt(data, f.off); err != nil && err != io.EOF {
				return nil, nil, err
			}
		}
		if start := int64(len(out)); start != f.off || int64(len(data)) != f.size {
			moved[f.off] = [2]int64{start, int64(len(data))}
		}
		out = append(out, data...)
		prev = f.off + f.size
	}
	tail := make([]byte, b.size-prev)
	if _, err := r.ReadAt(tail, prev); err != nil && err != io.EOF {
		return nil, nil, err
	}
	return append(out, tail...), moved, nil
}

// pcmHeader — заголовок сэмпла FSB5 для PCM16: частота и каналы, без чанков чужого кодека.
func pcmHeader(s FSBSample) []byte {
	le := binary.LittleEndian
	var chunks []byte
	add := func(typ uint32, body []byte) {
		chunks = le.AppendUint32(chunks, typ<<25|uint32(len(body))<<1)
		chunks = append(chunks, body...)
	}
	var m uint64
	rate := -1
	for i, r := range fsbRates {
		if r == s.Rate {
			rate = i
		}
	}
	if rate < 0 {
		rate = 0
		add(2, le.AppendUint32(nil, uint32(s.Rate)))
	}
	ch := -1
	for i, c := range []int{1, 2, 6, 8} {
		if c == s.Channels {
			ch = i
		}
	}
	if ch < 0 {
		ch = 0
		add(1, []byte{byte(s.Channels)})
	}
	m |= uint64(rate) << 1
	m |= uint64(ch) << 5
	if len(chunks) > 0 {
		m |= 1
		chunks = linkChunks(chunks)
	}
	return append(le.AppendUint64(nil, m), chunks...)
}

// linkChunks ставит у всех дополнительных чанков, кроме последнего, флаг «дальше ещё чанк».
func linkChunks(b []byte) []byte {
	le := binary.LittleEndian
	for p := 0; p+4 <= len(b); {
		x := le.Uint32(b[p:])
		n := int(x>>1) & 0xFFFFFF
		next := p + 4 + n
		if next < len(b) {
			le.PutUint32(b[p:], x|1)
		}
		p = next
	}
	return b
}
