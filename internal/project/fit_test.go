package project

import (
	"context"
	"math"
	"testing"

	"switch-translator/internal/audio"
	"switch-translator/internal/ffmpeg"
	"switch-translator/internal/nx"
)

// тон с тишиной по краям: lead с тишины, body с звука, tail с тишины
func tone(lead, body, tail float64) *audio.PCM {
	const rate = 22050
	n := int((lead + body + tail) * rate)
	ch := make([]int16, n)
	for i := int(lead * rate); i < int((lead+body)*rate); i++ {
		ch[i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*220/rate))
	}
	return &audio.PCM{Rate: rate, Channels: [][]int16{ch}}
}

func TestFitDub(t *testing.T) {
	if !ffmpeg.Available() {
		t.Skip("нет встроенного ffmpeg")
	}
	tmp := t.TempDir()
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.08 }

	// короче оригинала: только срезается тишина, не ускоряется
	_, f, err := fitDub(context.Background(), tone(0.5, 1, 0.5), 1.5, tmp)
	if err != nil || f.Speed != 1 || !near(f.Out, 1.08) || f.TooLong() {
		t.Errorf("короткая: %+v %v", f, err)
	}
	// чуть длиннее: ускоряется ровно до длины оригинала
	_, f, err = fitDub(context.Background(), tone(0.3, 2.4, 0.3), 2, tmp)
	if err != nil || f.Speed < 1.1 || !near(f.Out, 2) || f.TooLong() {
		t.Errorf("подгонка: %+v %v", f, err)
	}
	// намного длиннее: не больше MaxSpeed, и помечается как слишком длинная
	_, f, err = fitDub(context.Background(), tone(0, 4, 0), 1, tmp)
	if err != nil || f.Speed != MaxSpeed || !near(f.Out, 4.08/MaxSpeed) || !f.TooLong() {
		t.Errorf("предел: %+v %v", f, err)
	}
}

// В NSZ сжимаются только Program и PublicData, как у утилиты nsz: сжатый Control DBI не разбирает.
func TestCompressible(t *testing.T) {
	for typ, want := range map[nx.ContentType]bool{nx.ContentProgram: true, nx.ContentPublicData: true,
		nx.ContentControl: false, nx.ContentMeta: false, nx.ContentManual: false} {
		if compressible(typ) != want {
			t.Errorf("тип %d: %v", typ, !want)
		}
	}
}
