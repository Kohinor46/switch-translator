package project

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// Деление по голосам на готовых отпечатках (из кэша — без Python): имена групп, ручные голоса,
// переименование и повторное деление.
func TestSplitVoices(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	r := rand.New(rand.NewSource(3))
	const dim = 32
	centers := make([][]float32, 3)
	for v := range centers {
		centers[v] = make([]float32, dim)
		for d := range centers[v] {
			centers[v][d] = float32(r.NormFloat64())
		}
	}
	cache := map[string]voicePrint{}
	truth := map[string]int{}
	for v, n := range []int{30, 12, 6} {
		for k := range n {
			file := fmt.Sprintf("v%d_%02d.bwav", v, k)
			p := make([]float32, dim)
			var norm float32
			for d := range p {
				p[d] = centers[v][d] + 0.6*float32(r.NormFloat64())
				norm += p[d] * p[d]
			}
			for d := range p {
				p[d] /= float32(math.Sqrt(float64(norm)))
			}
			cache[file] = voicePrint{p, 2}
			truth[file] = v
			s.Audio = append(s.Audio, AudioLine{File: file, Recognized: true, Transcript: "фраза", Selected: true})
		}
	}
	// короткий вскрик: отпечатка нет — голос не определить
	cache["shout.bwav"] = voicePrint{nil, 0.1}
	s.Audio = append(s.Audio, AudioLine{File: "shout.bwav", Recognized: true, Transcript: "Ха!", Selected: true})
	if err := s.savePrints(cache); err != nil {
		t.Fatal(err)
	}
	all := make([]int, len(s.Audio))
	for i := range all {
		all[i] = i
	}
	split := func() {
		t.Helper()
		if err := s.SplitVoices(context.Background(), all, 0, func(string, float64, float64) {}); err != nil {
			t.Fatal(err)
		}
	}
	split()
	byVoice := map[int]string{}
	for _, l := range s.Audio {
		v, ok := truth[l.File]
		if !ok {
			if l.Speaker != "" {
				t.Fatalf("у вскрика голос %q", l.Speaker)
			}
			continue
		}
		if prev, seen := byVoice[v]; seen && prev != l.Speaker {
			t.Fatalf("голос %d разделился: %q и %q", v, prev, l.Speaker)
		}
		byVoice[v] = l.Speaker
	}
	if byVoice[0] != "Голос 1" || byVoice[1] != "Голос 2" || byVoice[2] != "Голос 3" {
		t.Fatalf("имена: %v", byVoice)
	}

	// персонажа назвали, одну реплику переложили руками — повторное деление это сохраняет
	if err := s.RenameSpeaker("Голос 1", "Ринг"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSpeakerVoice("Ринг", "xtts", "Ринг, скопированный\tclone:Ринг"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSpeaker(0, "Драгакс"); err != nil {
		t.Fatal(err)
	}
	split()
	if s.Audio[0].Speaker != "Драгакс" || s.Audio[1].Speaker != "Ринг" {
		t.Fatalf("после повтора: %q, %q", s.Audio[0].Speaker, s.Audio[1].Speaker)
	}
	if s.Audio[35].Speaker != "Голос 2" {
		t.Fatalf("второй голос стал %q", s.Audio[35].Speaker)
	}
	if v := s.SpeakerVoice("Ринг", "xtts"); v == "" {
		t.Fatal("голос озвучки пропал после переименования")
	}
	sp := s.Speakers()
	if len(sp) != 4 || sp[0].Name != "Ринг" || sp[0].Lines != 29 {
		t.Fatalf("список голосов: %+v", sp)
	}
	// слить голоса: переименовать один в имя другого
	if err := s.RenameSpeaker("Голос 3", "Голос 2"); err != nil {
		t.Fatal(err)
	}
	if sp := s.Speakers(); len(sp) != 3 || sp[1].Lines != 18 {
		t.Fatalf("после слияния: %+v", sp)
	}
}
