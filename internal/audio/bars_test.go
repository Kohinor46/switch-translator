package audio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Настоящие архивы: SWTR_BARS=<папка с .bars>. Разобранный и собранный без изменений архив
// совпадает с исходным байт в байт; после замены звука архив читается и звук в нём новый.
func TestBARSReal(t *testing.T) {
	dir := os.Getenv("SWTR_BARS")
	if dir == "" {
		t.Skip("SWTR_BARS не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.bars"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		a, err := ParseBARS(b)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Bytes(), b) {
			t.Errorf("%s: пересобранный архив не совпал", filepath.Base(f))
		}
		var target string
		for _, e := range a.Entries {
			if !e.Prefetch {
				target = e.Name
				break
			}
		}
		t.Logf("%s: %d звуков, первый %q", filepath.Base(f), len(a.Entries), a.Entries[0].Name)
		if target == "" {
			if _, err := ReplaceInBARS(b, a.Entries[0].Name, &PCM{Rate: 48000, Channels: [][]int16{make([]int16, 100)}}); err == nil {
				t.Error("в заготовку музыки звук вписываться не должен")
			}
			continue
		}
		p := &PCM{Rate: 48000, Channels: [][]int16{make([]int16, 96000)}}
		for i := range p.Channels[0] {
			p.Channels[0][i] = int16(i % 3000)
		}
		nb, err := ReplaceInBARS(b, target, p)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := DecodeBARSEntry(nb, target)
		if err != nil {
			t.Fatal(err)
		}
		if want := Resample(p, got.Rate); got.Samples() != want.Samples() || snr(want.Channels[0], got.Channels[0]) < 15 {
			t.Errorf("%s: в архиве не новый звук (%d сэмплов)", target, got.Samples())
		}
		// остальные звуки целы
		b2, _ := ParseBARS(nb)
		for i, e := range a.Entries {
			if e.Name != target && !bytes.Equal(bytes.TrimRight(e.BWAV, "\x00"), bytes.TrimRight(b2.Entries[i].BWAV, "\x00")) {
				t.Errorf("%s: звук %s изменился", filepath.Base(f), e.Name)
			}
		}
	}
}
