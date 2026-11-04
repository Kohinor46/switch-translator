package audio

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sine(n, rate int) *PCM {
	p := &PCM{Rate: rate, Channels: [][]int16{make([]int16, n)}}
	for i := range n {
		p.Channels[0][i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*440/float64(rate)))
	}
	return p
}

// encodeDSP — простейший DSP-ADPCM (один набор коэффициентов) только для проверки декодера.
func TestDSPDecoder(t *testing.T) {
	// коэффициенты 0: предсказание нулевое, тогда сэмпл = nib*scale
	d := dspInfo{}
	frame := []byte{0x02, 0x12, 0x34, 0x56, 0x70, 0x00, 0x00, 0xF0}
	got := decodeDSP(frame, 14, &d, nil)
	want := []int16{4, 8, 12, 16, 20, 24, 28, 0, 0, 0, 0, 0, 0, 0}
	want[13] = 0
	for i := range 7 {
		if got[i] != want[i] {
			t.Fatalf("сэмпл %d: %d, ждали %d (%v)", i, got[i], want[i], got)
		}
	}
	if got[12] != -4 {
		t.Fatalf("отрицательный полубайт: %v", got)
	}
}

func TestReplaceRoundTrip(t *testing.T) {
	dir := os.Getenv("SWTR_AUDIO_DIR")
	if dir == "" {
		t.Skip("SWTR_AUDIO_DIR не задан")
	}
	var files []string // не Glob: в путях к играм бывают «[ ]»
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bfstm" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		t.Skip("нет .bfstm")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		p, err := Decode(b)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		var peak int16
		for _, s := range p.Channels[0] {
			peak = max(peak, s, -s)
		}
		if p.Samples() == 0 || (peak < 100 && !strings.Contains(f, "Dummy")) {
			t.Fatalf("%s: тишина или пусто (%d сэмплов, пик %d)", filepath.Base(f), p.Samples(), peak)
		}
		if f == files[0] {
			t.Logf("%s: %d Гц, %d кан., %.1f с, цикл %v %d–%d", filepath.Base(f), p.Rate, len(p.Channels), p.Seconds(), p.Loop, p.LoopStart, p.LoopEnd)
			os.WriteFile(filepath.Join(t.TempDir(), "x.wav"), WAV(p), 0o644)
			if out := os.Getenv("SWTR_AUDIO_OUT"); out != "" {
				os.WriteFile(out, WAV(p), 0o644)
			}
		}
		// запись обратно и повторное чтение
		s := sine(50000, 24000)
		nb, err := Replace(b, s)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Decode(nb)
		if err != nil {
			t.Fatalf("%s: повторное чтение: %v", filepath.Base(f), err)
		}
		if back.Rate != 24000 || back.Samples() != 50000 || len(back.Channels) != len(p.Channels) {
			t.Fatalf("%s: после записи %d Гц, %d сэмплов, %d кан.", filepath.Base(f), back.Rate, back.Samples(), len(back.Channels))
		}
		for i := range 50000 {
			if back.Channels[len(back.Channels)-1][i] != s.Channels[0][i] {
				t.Fatalf("%s: сэмпл %d отличается", filepath.Base(f), i)
			}
		}
	}
	t.Logf("проверено %d файлов", len(files))
}

func TestWAVRoundTrip(t *testing.T) {
	s := sine(1000, 22050)
	p, err := ReadWAV(WAV(s))
	if err != nil || p.Rate != 22050 || p.Samples() != 1000 || p.Channels[0][10] != s.Channels[0][10] {
		t.Fatalf("WAV: %v %+v", err, p)
	}
	if m := Mono16k(s); len(m) != 1000*16000/22050 {
		t.Fatalf("Mono16k: %d", len(m))
	}
}
