package project

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"switch-translator/internal/gametext"
)

func TestCollectTerms(t *testing.T) {
	texts := []string{
		"Let's go after Dragaux!", "You know Dragaux's plan?", "You can do it.", "Thank you, you rock.",
		"Open {{0:3:ab}}My Menu{{0:3:}} now.", "Check My Menu.", "Ms. Honey is here.", "Hi, Ms. Honey!",
		"Press A to jump.", "Press ZL to dodge.", "Please open it and check.", "I'm fine. I'm fine.", "トードがいます。トードです",
	}
	terms, st := CollectTerms(texts)
	got := map[string]int{}
	for _, tm := range terms {
		got[tm.Source] = tm.Count
	}
	for _, w := range []string{"Dragaux", "My Menu", "Ms. Honey", "トード"} {
		if got[w] != 2 {
			t.Errorf("нет термина %q: %v", w, got)
		}
	}
	for _, w := range []string{"You", "Press", "I'm", "ZL", "Thank"} {
		if _, ok := got[w]; ok {
			t.Errorf("лишний термин %q", w)
		}
	}
	if st.Words == 0 || st.Unique >= st.Words {
		t.Errorf("слова: %+v", st)
	}
}

// Речь и реплика с одинаковым текстом переводятся одним запросом, перевод вписывается в обе.
func TestTranslateSpeechWithText(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	s.Strings = []gametext.String{{ID: "1", Source: "Ready?"}, {ID: "2", Source: "Go!"}}
	s.Audio = []AudioLine{
		{File: "a", Selected: true, Recognized: true, Transcript: "Ready?"},
		{File: "b", Selected: true, Recognized: true, Transcript: "Nice job!"},
		{File: "c", Selected: false, Recognized: true, Transcript: "Skipped"},
		{File: "d", Selected: true, Recognized: true, Transcript: "Go!", Translation: "Вперёд!", HandTranslation: true},
	}
	tr := &countTr{}
	if _, err := s.TranslateText(context.Background(), tr, "ru", func(string, float64, float64) {}); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) != 2 || tr.sent[0] != "Ready?" || tr.sent[1] != "Nice job!" {
		t.Fatalf("переводчику ушло %q", tr.sent)
	}
	if s.Audio[0].Translation != "ru:Ready?" || s.Strings[0].Translation != "ru:Ready?" || s.Audio[2].Translation != "" {
		t.Errorf("%+v %+v", s.Audio, s.Strings)
	}
	if s.Strings[1].Translation != "Вперёд!" {
		t.Errorf("ручной перевод речи не подхвачен: %q", s.Strings[1].Translation)
	}
}

// Ручная правка: новый текст речи сбрасывает автоматический перевод и озвучку голосом, своя запись остаётся.
func TestEditSpeech(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	s.Audio = []AudioLine{
		{File: "a", Recognized: true, Transcript: "Redy", Translation: "Рэди", Dub: "a.wav"},
		{File: "b", Recognized: true, Transcript: "Go", Translation: "Вперёд", Dub: "own.wav", HandDub: true},
	}
	if err := s.EditSpeech(0, "Ready?", "Рэди"); err != nil {
		t.Fatal(err)
	}
	if a := s.Audio[0]; !a.HandText || a.Translation != "" || a.Dub != "" || !a.Selected {
		t.Errorf("%+v", a)
	}
	s.EditSpeech(1, "Go!", "Вперёд!")
	if b := s.Audio[1]; !b.HandTranslation || b.Dub != "own.wav" || b.Translation != "Вперёд!" {
		t.Errorf("%+v", b)
	}
}

// audio.json хранит работу со всеми языками озвучки: сохранение списка одного языка не стирает другой.
func TestSaveAudioKeepsOtherLocales(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	s.Audio = []AudioLine{{File: "Voice.USen.pack::a", Transcript: "Hi"}}
	s.SaveAudio()
	s.Audio = []AudioLine{{File: "Voice.JPja.pack::a", Transcript: "やあ"}}
	s.SaveAudio()
	s.Audio = nil
	s.Report = nil
	b, _ := os.ReadFile(s.audioPath())
	var all []AudioLine
	json.Unmarshal(b, &all)
	if len(all) != 2 {
		t.Fatalf("%+v", all)
	}
}

// Заевшее распознавание чинится при открытии: перевод и озвучка сбрасываются, ручные правки не трогаются.
func TestFixLoops(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	loop := "Flutter kick Flutter kick Flutter kick Flutter kick"
	s.Audio = []AudioLine{
		{File: "a", Recognized: true, Transcript: loop, Translation: "Ножницы Ножницы Ножницы Ножницы", Dub: "a.wav"},
		{File: "b", Recognized: true, Transcript: loop, Translation: "Ножницы", HandTranslation: true, Dub: "b.wav", HandDub: true},
		{File: "c", Recognized: true, Transcript: loop, HandText: true},
		{File: "d", Recognized: true, Transcript: "Keep going!", Translation: "Дальше!", Dub: "d.wav"},
	}
	s.fixLoops()
	a, b, c, d := s.Audio[0], s.Audio[1], s.Audio[2], s.Audio[3]
	if a.Transcript != "Flutter kick" || a.Translation != "" || a.Dub != "" {
		t.Errorf("a: %+v", a)
	}
	if b.Transcript != "Flutter kick" || b.Translation != "Ножницы" || b.Dub != "b.wav" {
		t.Errorf("b: %+v", b)
	}
	if c.Transcript != loop || d.Dub != "d.wav" {
		t.Errorf("c/d: %+v %+v", c, d)
	}
}

// Крик «AHHHH…» на сотни букв укорачивается при открытии и в переводе — XTTS такие не берёт.
func TestFixStretched(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	s.Audio = []AudioLine{
		{File: "a", Recognized: true, Transcript: "AH" + strings.Repeat("H", 800), Translation: strings.Repeat("А", 900) + "!"},
	}
	s.fixLoops()
	if a := s.Audio[0]; a.Transcript != "AHHHHH" || a.Translation != "ААААА!" {
		t.Fatalf("%+v", a)
	}
}
