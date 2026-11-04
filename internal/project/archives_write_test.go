package project

// Запись озвучки в звуковые архивы настоящих игр: SWTR_W=<папка с играми> (NSMBU Deluxe — BFSAR, Kirby — BFSAR в LZ4,
// Captain Toad — группы BFGRP в SARC, Mario Kart 8 — BARS с BFWAV). Сборка вписывает тон короче и длиннее оригинала,
// и из собранного мода он читается той же длины.

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"switch-translator/internal/audio"
	"switch-translator/internal/codec"
	"switch-translator/internal/gametext"
	"switch-translator/internal/keys"
)

func TestWriteArchives(t *testing.T) {
	root := os.Getenv("SWTR_W")
	if root == "" {
		t.Skip("SWTR_W не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	ks.LoadTitleKeys(filepath.Join(home, ".switch", "title.keys"))
	for _, c := range []struct{ game, want string }{
		{"New Super Mario Bros U Deluxe [NSZ]/New Super Mario Bros U Deluxe [0100EA80032EA000][v0] (2.50 GB).nsz", "звук в архиве Sound.bfsar"},
		{"Kirby and the Forgotten Land [NSP]/Kirby and the Forgotten Land [01004D300C5AE000][v0].nsp", "BossLionVoice.bfsar.cmpbin"},
		{"Captain Toad Treasure Tracker [NSZ]/Captain Toad Treasure Tracker [01009BF0072D4000][v0] (1.48 GB).nsz", "звук в группе"},
		{"Mario Kart 8 Deluxe [NSZ]/Mario Kart 8 Deluxe [0100152000022000][v0] (6.77 GB).nsz", "FWAV в архиве BARS"},
	} {
		game := filepath.Join(root, c.game)
		work := t.TempDir()
		s, err := Analyze(game, FindExtras(game), ks, work, func(string, ...any) {}, func(string, float64, float64) {})
		if err != nil {
			t.Fatal(err)
		}
		if locs := s.Locales(); len(locs) > 0 {
			s.SelectLocale(s.DefaultLocale("en"))
		}
		var idx []int
		var lens []float64
		for i, l := range s.Audio {
			if len(idx) == 2 || !strings.Contains(l.Format, c.want) && !strings.Contains(l.File, c.want) {
				continue
			}
			p, err := s.decodeAudio(context.Background(), l.File)
			if err != nil || p.Seconds() < 0.4 {
				continue
			}
			scale := []float64{0.7, 1.3}[len(idx)]
			n := int(p.Seconds() * scale * 22050)
			tone := &audio.PCM{Rate: 22050, Channels: [][]int16{make([]int16, n)}}
			for j := range tone.Channels[0] {
				tone.Channels[0][j] = int16(8000 * math.Sin(2*math.Pi*350*float64(j)/22050))
			}
			dub := filepath.Join(work, "dub", s.audioBase(l.File)+".wav")
			writeFile(dub, audio.WAV(tone))
			s.Audio[i].Dub, s.Audio[i].Selected = dub, true
			idx = append(idx, i)
			lens = append(lens, float64(n)/22050)
		}
		if len(idx) < 2 {
			t.Errorf("%s: подходящих звуков %d", c.want, len(idx))
			s.Close()
			continue
		}
		var logs []string
		s.log = func(f string, a ...any) { logs = append(logs, f) }
		res, err := s.Build(BuildOptions{To: "ru", Audio: true, FitDubs: false}, func(string, float64, float64) {})
		if err != nil {
			t.Fatalf("%s: %v", c.want, err)
		}
		if res.Dubs != 2 || len(res.Warnings) > 0 {
			t.Errorf("%s: озвучек %d, предупреждения %v", c.want, res.Dubs, res.Warnings)
		}
		for k, i := range idx {
			data, err := modSound(s, s.Audio[i].File)
			if err != nil {
				t.Fatalf("%s: %v", s.Audio[i].File, err)
			}
			p, err := audio.Decode(data)
			if err != nil {
				t.Fatalf("%s: %v", s.Audio[i].File, err)
			}
			t.Logf("%s: %.2f с при %d Гц (ждали %.2f с)", s.Audio[i].File, p.Seconds(), p.Rate, lens[k])
			if math.Abs(p.Seconds()-lens[k]) > 0.03 {
				t.Errorf("%s: в моде %.2f с, а вписано %.2f с", s.Audio[i].File, p.Seconds(), lens[k])
			}
		}
		s.Close()
	}
}

// modSound — байты звука из собранного мода (как audioData, но из файлов мода).
func modSound(s *Session, file string) ([]byte, error) {
	archive, name, _ := strings.Cut(file, barsSep)
	outer, inner, nested := strings.Cut(archive, archSep)
	b, err := os.ReadFile(s.ModPath(outer))
	if err != nil {
		return nil, err
	}
	if b, _, err = codec.Unwrap(b); err != nil {
		return nil, err
	}
	if nested {
		files, err := gametext.ArchiveFiles(b)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Name == inner {
				b, _, err = codec.Unwrap(f.Data)
			}
		}
	}
	switch {
	case audio.IsBFSAR(b):
		a, err := audio.ParseBFSAR(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		i, _ := a.Find(name)
		return a.Wave(bytes.NewReader(b), i)
	case string(b[:4]) == "FGRP":
		return audio.FGRPWave(b, name)
	default:
		a, err := audio.ParseBARS(b)
		if err != nil {
			return nil, err
		}
		i, _ := a.Find(name)
		return a.Sound(i), nil
	}
}
