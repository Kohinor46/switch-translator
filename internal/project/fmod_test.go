package project

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"switch-translator/internal/audio"
	"switch-translator/internal/keys"
)

func TestBankTag(t *testing.T) {
	for p, want := range map[string]string{
		"PPA/Content/FMOD/Switch/Dialogues_ENG.bank": "ENG",
		"Audio/VO-fr.bank":                           "FR",
		"Audio/Music_Theme.bank":                     "",
		"Audio/Character_MISC.bank":                  "",
		"Audio/Ambience_Zone_00.bank":                "",
	} {
		if _, tag, _ := bankTag(p); tag != want {
			t.Errorf("%s: язык %q, ждали %q", p, tag, want)
		}
	}
	paths := []string{"D/Dialogues_EUK.bank", "D/Dialogues_ENG.bank", "D/Dialogues_MEX.bank", "D/Dialogues_SPA.bank"}
	if got := pickLangBank(paths, "en"); got != "D/Dialogues_ENG.bank" {
		t.Errorf("английский: %s", got)
	}
	if got := pickLangBank(paths, "es"); got != "D/Dialogues_SPA.bank" {
		t.Errorf("испанский: %s", got)
	}
	if got := pickLangBank(paths, "ja"); got != "" {
		t.Errorf("японского нет, а выбран %s", got)
	}
}

// Настоящая игра на FMOD: SWTR_FMOD_GAME=<образ> (например, PAW Patrol World). Банки разбираются на звуки,
// из банков диалогов виден только язык оригинала, реплика расшифровывается, а сборка вписывает озвучку в банк.
func TestFMODGame(t *testing.T) {
	game := os.Getenv("SWTR_FMOD_GAME")
	if game == "" {
		t.Skip("SWTR_FMOD_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, err := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	began := time.Now()
	s, err := Analyze(game, FindExtras(game), ks, work, t.Logf, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SelectLang("en")
	t.Logf("анализ и список звука: %v, звуков %d", time.Since(began).Round(time.Millisecond), len(s.Audio))
	count := func(sub string) (n int) {
		for _, l := range s.Audio {
			if strings.Contains(l.File, sub) {
				n++
			}
		}
		return n
	}
	if count("Dialogues_ENG.bank#") < 1000 || count("Dialogues_FRA") > 0 || count("Dialogues_EUK") > 0 {
		t.Fatalf("английских реплик %d, французских %d, британских %d", count("Dialogues_ENG.bank#"), count("Dialogues_FRA"), count("Dialogues_EUK"))
	}
	s.SelectLang("fr")
	if count("Dialogues_FRA.bank#") < 1000 || count("Dialogues_ENG") > 0 {
		t.Fatalf("после выбора французского: французских %d, английских %d", count("Dialogues_FRA.bank#"), count("Dialogues_ENG"))
	}
	s.SelectLang("ru") // русской озвучки нет — показывается английская
	if count("Dialogues_ENG.bank#") < 1000 {
		t.Fatalf("без русской озвучки нет и английской")
	}
	s.SelectLang("en")

	var idx []int
	for i, l := range s.Audio {
		if strings.Contains(l.File, "Dialogues_ENG.bank#CH_02_Playground_SPK_2") {
			idx = append(idx, i)
		}
	}
	if len(idx) < 2 {
		t.Fatalf("реплик CH_02_Playground_SPK_2*: %d", len(idx))
	}
	wav, err := s.AudioWav(context.Background(), s.Audio[idx[0]].File)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(wav)
	p, err := audio.ReadWAV(b)
	if err != nil || p.Seconds() < 0.3 {
		t.Fatalf("реплика не расшифровалась: %v", err)
	}
	t.Logf("%s: %.1f с, %d Гц", s.Audio[idx[0]].File, p.Seconds(), p.Rate)
	if kind, _ := audio.Features(p).Voice(); kind != "голос" {
		t.Logf("«Найти голос» считает реплику: %s", kind)
	}

	// озвучка двух реплик: тон 24 кГц стерео — сборка переводит в частоту и каналы банка
	for k, i := range idx[:2] {
		tone := &audio.PCM{Rate: 24000, Channels: [][]int16{make([]int16, 24000), make([]int16, 24000)}}
		for j := range tone.Channels[0] {
			v := int16(9000 * math.Sin(2*math.Pi*float64(300+100*k)*float64(j)/24000))
			tone.Channels[0][j], tone.Channels[1][j] = v, v
		}
		dub := filepath.Join(work, "dub", s.audioBase(s.Audio[i].File)+".wav")
		if err := writeFile(dub, audio.WAV(tone)); err != nil {
			t.Fatal(err)
		}
		s.Audio[i].Dub, s.Audio[i].Selected = dub, true
	}
	res, err := s.Build(BuildOptions{To: "ru", Audio: true, FitDubs: false}, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dubs != 2 || len(res.Warnings) > 0 {
		t.Fatalf("озвучек %d, предупреждения %v", res.Dubs, res.Warnings)
	}
	bank, name, _ := splitBank(s.Audio[idx[0]].File)
	nb, err := os.ReadFile(s.ModPath(bank))
	if err != nil {
		t.Fatal(err)
	}
	fb, err := audio.ParseFMOD(bytes.NewReader(nb), int64(len(nb)))
	if err != nil {
		t.Fatalf("собранный банк не читается: %v", err)
	}
	k, ok := fb.Find(name)
	if !ok {
		t.Fatalf("в собранном банке нет %s", name)
	}
	one, _ := fb.Sample(bytes.NewReader(nb), k)
	got, err := audio.Decode(one)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rate != p.Rate || len(got.Channels) != len(p.Channels) || math.Abs(got.Seconds()-1) > 0.01 {
		t.Errorf("вписано %d Гц × %d, %.2f с — ждали %d Гц × %d, 1 с", got.Rate, len(got.Channels), got.Seconds(), p.Rate, len(p.Channels))
	}
	t.Logf("собран %s: %d МБ, %d звуков", filepath.Base(bank), len(nb)>>20, len(fb.Samples))
}
