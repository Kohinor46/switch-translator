package audio

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"
)

// snr — отношение сигнал/шум в дБ.
func snr(a, b []int16) float64 {
	var s, n float64
	for i := range a {
		s += float64(a[i]) * float64(a[i])
		d := float64(a[i]) - float64(b[i])
		n += d * d
	}
	return 10 * math.Log10(s/math.Max(n, 1))
}

// Сжатое DSP-ADPCM читается нашим же декодером и звучит как исходник.
func TestEncodeDSP(t *testing.T) {
	src := sine(10007, 28000).Channels[0]
	for i := range src { // плюс шум и щелчок — как в речи
		src[i] += int16((i*7919)%600 - 300)
	}
	src[5000] = 30000
	d := dspInfo{coefs: dspDefaultCoefs}
	got := decodeDSP(encodeDSP(src, dspDefaultCoefs), len(src), &d, nil)
	if len(got) != len(src) {
		t.Fatalf("%d сэмплов из %d", len(got), len(src))
	}
	if q := snr(src, got); q < 20 {
		t.Errorf("сигнал/шум %.1f дБ", q)
	}
}

// BWAV как у Ring Fit: моно DSP 28 кГц, цикл «до конца». Новый звук — тоже DSP 28 кГц,
// с CRC данных в заголовке; в BARS длина в AMTA меняется вместе со звуком.
func TestReplaceBWAVLikeNintendo(t *testing.T) {
	le := binary.LittleEndian
	orig := make([]byte, 0x80+0x40)
	copy(orig, "BWAV\xff\xfe\x01\x00")
	le.PutUint16(orig[0xE:], 1)
	h := 0x10
	le.PutUint16(orig[h:], 1)
	le.PutUint16(orig[h+2:], 2)
	le.PutUint32(orig[h+4:], 28000)
	le.PutUint32(orig[h+8:], 14)
	le.PutUint32(orig[h+0xC:], 14)
	for k, v := range [16]int16{1600, -700, 2900, -1500, 500, 300, 3200, -1700, 0, 0, 2048, 0, 1024, 1024, 3800, -1900} {
		le.PutUint16(orig[h+0x10+k*2:], uint16(v))
	}
	le.PutUint32(orig[h+0x30:], 0x80)
	le.PutUint32(orig[h+0x34:], 0x80)
	le.PutUint32(orig[h+0x38:], 1)
	le.PutUint32(orig[h+0x3C:], 0xFFFFFFFF)

	nb, err := Replace(orig, sine(22050, 22050))
	if err != nil {
		t.Fatal(err)
	}
	if c := le.Uint16(nb[h:]); c != 1 {
		t.Fatalf("кодек %d, а не DSP", c)
	}
	if r, n := le.Uint32(nb[h+4:]), le.Uint32(nb[h+0xC:]); r != 28000 || n != 28000 {
		t.Fatalf("частота %d, сэмплов %d", r, n)
	}
	if le.Uint32(nb[h+0x38:]) != 1 || le.Uint32(nb[h+0x3C:]) != 0xFFFFFFFF || le.Uint16(nb[h+2:]) != 2 {
		t.Errorf("цикл и панорама не как у оригинала")
	}
	start := int(le.Uint32(nb[h+0x34:]))
	if crc := crc32.ChecksumIEEE(nb[start : start+dspFrameBytes(28000)]); le.Uint32(nb[8:]) != crc {
		t.Errorf("CRC %08x, а данных %08x", le.Uint32(nb[8:]), crc)
	}
	if len(nb)%0x40 != 0 || len(nb) > 0x80+dspFrameBytes(28000)+0x40 {
		t.Errorf("размер %d", len(nb))
	}
	p, err := Decode(nb)
	if err != nil {
		t.Fatal(err)
	}
	want := Resample(sine(22050, 22050), 28000).Channels[0]
	if q := snr(want, p.Channels[0]); q < 20 {
		t.Errorf("сигнал/шум %.1f дБ", q)
	}

	// BARS из двух имён на один звук и третьего отдельного
	bars := buildTestBARS(t, orig)
	out, err := ReplaceInBARS(bars, "a", sine(22050, 22050)) // у «a» данных нет: они у «b»
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseBARS(out)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range []uint32{28000, 28000, 14} {
		d := a.amta[k] + 0x20
		if got := le.Uint32(out[d+0x20:]); got != want {
			t.Errorf("AMTA звука %d: %d сэмплов, а надо %d", k, got, want)
		}
	}
}

// buildTestBARS — архив: звуки a и b — один BWAV, c — свой.
func buildTestBARS(t *testing.T, bwav []byte) []byte {
	le := binary.LittleEndian
	const n = 3
	amta := func(name string) []byte {
		b := make([]byte, 0x20+0x64+8+8)
		copy(b, "AMTA\xff\xfe\x00\x04")
		le.PutUint32(b[0xC:], 0x20)
		le.PutUint32(b[0x18:], 0x20+0x64)
		d := b[0x20:]
		copy(d, "DATA")
		le.PutUint32(d[4:], 0x64)
		le.PutUint32(d[0x18:], 28000)
		le.PutUint32(d[0x20:], 14)
		s := b[0x20+0x64:]
		copy(s, "STRG")
		le.PutUint32(s[4:], 2)
		copy(s[8:], name)
		return b
	}
	head := make([]byte, 0x10+n*4+n*8)
	copy(head, "BARS")
	copy(head[8:], "\xff\xfe")
	le.PutUint32(head[0xC:], n)
	var am [][]byte
	for _, name := range []string{"a", "b", "c"} {
		am = append(am, amta(name))
	}
	out := head
	offs := make([]int, n)
	for i, x := range am {
		offs[i] = len(out)
		out = append(out, x...)
	}
	for len(out)%0x40 != 0 {
		out = append(out, 0)
	}
	w1 := len(out)
	out = append(out, bwav...)
	w2 := len(out)
	out = append(out, bwav...)
	for i, w := range []int{w1, w1, w2} {
		le.PutUint32(out[0x10+n*4+i*8:], uint32(offs[i]))
		le.PutUint32(out[0x10+n*4+i*8+4:], uint32(w))
	}
	le.PutUint32(out[4:], uint32(len(out)))
	if _, err := ParseBARS(out); err != nil {
		t.Fatal(err)
	}
	return out
}
