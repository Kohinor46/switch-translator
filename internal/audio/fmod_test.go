package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func tone(n, rate int, hz float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(12000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

// snrCommon — сигнал/шум в дБ по общей части (длины могут не совпадать).
func snrCommon(a, b []int16) float64 {
	var sig, noise float64
	for i := range min(len(a), len(b)) {
		sig += float64(a[i]) * float64(a[i])
		d := float64(a[i]) - float64(b[i])
		noise += d * d
	}
	return 10 * math.Log10(sig/max(noise, 1))
}

func TestFADPCMRoundTrip(t *testing.T) {
	src := tone(5000, 32000, 440)
	for i := range src { // шум и скачок громкости — не только чистый тон
		src[i] += int16((i * 7919 % 2001) - 1000)
		if i > 3000 {
			src[i] /= 8
		}
	}
	data := encodeFADPCM([][]int16{src})
	if len(data) != 20*fadpcmFrame {
		t.Fatalf("кадров %d, а нужно 20", len(data)/fadpcmFrame)
	}
	got := decodeFADPCM(data, 0, 1, len(src))
	if len(got) != len(src) {
		t.Fatalf("сэмплов %d, а нужно %d", len(got), len(src))
	}
	if q := snrCommon(src, got); q < 20 {
		t.Errorf("FADPCM: сигнал/шум %.1f дБ — слишком грубо", q)
	}
	// стерео: каналы чередуются покадрово
	right := tone(600, 32000, 1000)
	st := encodeFADPCM([][]int16{src[:600], right})
	if q := snrCommon(right, decodeFADPCM(st, 1, 2, 600)); q < 20 {
		t.Errorf("FADPCM стерео: правый канал %.1f дБ", q)
	}
}

// Настоящие банки: SWTR_FMOD=<папка с .bank>. Звук, вынутый отдельным FSB5, расшифровывается так же,
// как его читает vgmstream из целого банка; банк с заменённым звуком читается, остальные звуки — прежние.
func TestFMODReal(t *testing.T) {
	dir := os.Getenv("SWTR_FMOD")
	if dir == "" {
		t.Skip("SWTR_FMOD не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.bank"))
	vgm, _ := exec.LookPath("vgmstream-cli")
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		b, err := ParseFMOD(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Logf("%s: %v", filepath.Base(f), err)
			continue
		}
		i := len(b.Samples) / 2
		s := b.Samples[i]
		one, err := b.Sample(bytes.NewReader(raw), i)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: звуков %d, кодек %d; №%d «%s» %d Гц × %d, %d сэмплов", filepath.Base(f), len(b.Samples), s.Codec, i, s.Name, s.Rate, s.Channels, s.Samples)
		if !Supported(one) {
			continue
		}
		p, err := Decode(one)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if p.Samples() != s.Samples {
			t.Errorf("%s: сэмплов %d, а в заголовке %d", s.Name, p.Samples(), s.Samples)
		}
		if vgm != "" {
			out := filepath.Join(t.TempDir(), "v.wav")
			if err := exec.Command(vgm, "-s", strconv.Itoa(i+1), "-o", out, f).Run(); err != nil {
				t.Fatalf("vgmstream: %v", err)
			}
			wb, _ := os.ReadFile(out)
			v, err := ReadWAV(wb)
			if err != nil {
				t.Fatal(err)
			}
			for c := range p.Channels {
				if !slicesEqual(p.Channels[c], v.Channels[c]) {
					t.Errorf("%s: канал %d расшифрован не так, как у vgmstream (%.1f дБ)", s.Name, c, snrCommon(v.Channels[c], p.Channels[c]))
				}
			}
		}

		// замена: новый звук вдвое длиннее, банк читается, у остальных звуков данные прежние
		long := &PCM{Rate: s.Rate, Channels: [][]int16{tone(s.Samples*2+77, s.Rate, 330)}}
		nb, err := ReplaceInFMOD(bytes.NewReader(raw), b, map[string]*PCM{s.Name: long})
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		b2, err := ParseFMOD(bytes.NewReader(nb), int64(len(nb)))
		if err != nil {
			t.Fatalf("пересобранный %s не читается: %v", filepath.Base(f), err)
		}
		if len(b2.Samples) != len(b.Samples) {
			t.Fatalf("звуков стало %d", len(b2.Samples))
		}
		for k := range b.Samples {
			if k == i {
				continue
			}
			o, n := b.Samples[k], b2.Samples[k]
			if o.Name != n.Name || !bytes.Equal(raw[o.off:o.off+o.size], nb[n.off:n.off+n.size]) {
				t.Fatalf("звук %d «%s» изменился", k, o.Name)
			}
		}
		one2, _ := b2.Sample(bytes.NewReader(nb), i)
		p2, err := Decode(one2)
		if err != nil {
			t.Fatal(err)
		}
		if p2.Samples() != len(long.Channels[0]) || snrCommon(long.Channels[0], p2.Channels[0]) < 20 {
			t.Errorf("%s: новый звук записан не так (%d сэмплов, %.1f дБ)", s.Name, p2.Samples(), snrCommon(long.Channels[0], p2.Channels[0]))
		}
		if vgm != "" {
			nf := filepath.Join(t.TempDir(), filepath.Base(f))
			os.WriteFile(nf, nb, 0o644)
			out := filepath.Join(t.TempDir(), "n.wav")
			if msg, err := exec.Command(vgm, "-s", strconv.Itoa(i+1), "-o", out, nf).CombinedOutput(); err != nil {
				t.Fatalf("vgmstream не читает пересобранный банк: %v %s", err, msg)
			}
			wb, _ := os.ReadFile(out)
			v, _ := ReadWAV(wb)
			if v == nil || !slicesEqual(v.Channels[0], p2.Channels[0]) {
				t.Errorf("%s: vgmstream читает новый звук иначе", s.Name)
			}
		}
	}
}

func slicesEqual(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Настоящие архивы: SWTR_BFSAR=<файл .bfsar>. Звуки находятся, у волновых архивов есть имена, звуки читаются.
func TestBFSARReal(t *testing.T) {
	f := os.Getenv("SWTR_BFSAR")
	if f == "" {
		t.Skip("SWTR_BFSAR не задан")
	}
	raw, _ := os.ReadFile(f)
	a, err := ParseBFSAR(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, w := range a.Waves {
		if !strings.HasPrefix(w.Name, "WAR_") {
			named++
		}
	}
	t.Logf("звуков %d, с именем архива %d; %s … %s", len(a.Waves), named, a.Waves[0].Name, a.Waves[len(a.Waves)-1].Name)
	bad := 0
	var secs float64
	for i := range a.Waves {
		b, _ := a.Wave(bytes.NewReader(raw), i)
		p, err := Decode(b)
		if err != nil || p.Samples() == 0 {
			bad++
			continue
		}
		secs += p.Seconds()
	}
	if bad > 0 {
		t.Errorf("не прочиталось %d звуков из %d", bad, len(a.Waves))
	}
	t.Logf("всего %.0f с звука", secs)
}

// Запись в BFSAR на месте: звук вдвое длиннее, чем был, на 1,4 раза — частота понижается, размер архива и
// остальные звуки не меняются, новый звук читается и нашим декодером, и vgmstream.
func TestBFSARWrite(t *testing.T) {
	f := os.Getenv("SWTR_BFSAR")
	if f == "" {
		t.Skip("SWTR_BFSAR не задан")
	}
	raw, _ := os.ReadFile(f)
	a, err := ParseBFSAR(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var picks []int
	for i, w := range a.Waves {
		if b, _ := a.Wave(bytes.NewReader(raw), i); OrigRate(b) > 0 && w.size > 20000 {
			if p, err := Decode(b); err == nil && p.Seconds() > 0.5 {
				picks = append(picks, i)
			}
		}
		if len(picks) == 3 {
			break
		}
	}
	data := append([]byte(nil), raw...)
	var waves []SARWave
	var sounds []*PCM
	var lens []float64
	for k, i := range picks {
		b, _ := a.Wave(bytes.NewReader(raw), i)
		p, _ := Decode(b)
		scale := []float64{0.6, 1.0, 1.4}[k]
		n := int(float64(p.Samples()) * scale)
		lens = append(lens, p.Seconds()*scale)
		waves = append(waves, a.Waves[i])
		sounds = append(sounds, &PCM{Rate: p.Rate, Channels: [][]int16{tone(n, p.Rate, 400)}})
	}
	rates, err := ReplaceWavesInPlace(data, waves, sounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(raw) {
		t.Fatal("размер архива изменился")
	}
	a2, err := ParseBFSAR(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(a2.Waves) != len(a.Waves) {
		t.Fatalf("архив после записи: %v", err)
	}
	changed := map[int]bool{}
	for _, i := range picks {
		changed[i] = true
	}
	for i, w := range a.Waves {
		same := bytes.Equal(raw[w.off:w.off+w.size], data[w.off:w.off+w.size])
		if same == changed[i] {
			t.Fatalf("звук %s: изменён %v, а ждали %v", w.Name, !same, changed[i])
		}
	}
	vgm, _ := exec.LookPath("vgmstream-cli")
	for k, i := range picks {
		b, _ := a2.Wave(bytes.NewReader(data), i)
		p, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.2f с при %d Гц (ждали %.2f с)", a.Waves[i].Name, p.Seconds(), rates[k], lens[k])
		if math.Abs(p.Seconds()-lens[k]) > 0.02 || p.Rate != rates[k] {
			t.Errorf("%s: длина %.2f с, частота %d", a.Waves[i].Name, p.Seconds(), p.Rate)
		}
		if vgm != "" {
			in := filepath.Join(t.TempDir(), "w.bfwav")
			os.WriteFile(in, b, 0o644)
			out := filepath.Join(t.TempDir(), "w.wav")
			if msg, err := exec.Command(vgm, "-o", out, in).CombinedOutput(); err != nil {
				t.Fatalf("vgmstream: %v %s", err, msg)
			}
			wb, _ := os.ReadFile(out)
			v, _ := ReadWAV(wb)
			if v == nil || !slicesEqual(v.Channels[0], p.Channels[0]) {
				t.Errorf("%s: vgmstream читает иначе", a.Waves[i].Name)
			}
		}
	}
	if rates[2] >= OrigRate(raw[a.Waves[picks[2]].off:]) {
		t.Errorf("длинный звук записан без понижения частоты")
	}
}

// Файл DSP с заголовком в обратном порядке байт (реплики LEGO): тон записывается и читается обратно.
func TestDSPFile(t *testing.T) {
	src := &PCM{Rate: 24000, Channels: [][]int16{tone(24005, 24000, 440)}}
	orig := make([]byte, dspHeader+16)
	le := binary.LittleEndian
	le.PutUint32(orig, 14)
	le.PutUint32(orig[4:], 16)
	le.PutUint32(orig[8:], 24000)
	copy(orig[0x1C:], func() []byte {
		b := make([]byte, 32)
		for i, c := range dspDefaultCoefs {
			le.PutUint16(b[i*2:], uint16(c))
		}
		return b
	}())
	if !IsDSPFile(orig) {
		t.Fatal("заготовка не узнаётся")
	}
	out, err := ReplaceDSPFile(orig, src)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(out)
	if err != nil || p.Rate != 24000 || len(p.Channels[0]) != len(src.Channels[0]) {
		t.Fatalf("%v", err)
	}
	if snr := snrCommon(src.Channels[0], p.Channels[0]); snr < 15 {
		t.Fatalf("SNR %.1f дБ", snr)
	}
	if n := len(src.Channels[0]); le.Uint32(out[4:]) != uint32(n/14*16+n%14+2) {
		t.Fatalf("полубайтов %d", le.Uint32(out[4:]))
	}
}

// Настоящая реплика: SWTR_DSP=<файл .adpcm> (LEGO). Сколько сэмплов и какой громкости.
func TestDSPReal(t *testing.T) {
	p := os.Getenv("SWTR_DSP")
	if p == "" {
		t.Skip("SWTR_DSP не задан")
	}
	b, _ := os.ReadFile(p)
	pcm, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	var peak int16
	for _, v := range pcm.Channels[0] {
		peak = max(peak, v, -v)
	}
	t.Logf("%d Гц, %d сэмплов, пик %d", pcm.Rate, len(pcm.Channels[0]), peak)
	out, err := ReplaceDSPFile(b, pcm)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Decode(out)
	t.Logf("после перезаписи: SNR %.1f дБ", snrCommon(pcm.Channels[0], again.Channels[0]))
}
