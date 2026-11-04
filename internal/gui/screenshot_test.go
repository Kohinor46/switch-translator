package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"switch-translator/internal/audio"
	"switch-translator/internal/speech"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"switch-translator/internal/i18n"
	"switch-translator/internal/keys"
	"switch-translator/internal/project"
)

// TestScreenshots рисует все вкладки без экрана — для проверки вёрстки.
// SWTR_SHOT_GAME=игра.nsz SWTR_SHOT_DIR=папка [SWTR_SHOT_KEYS=prod.keys] go test ./internal/gui -run Screenshots
func TestScreenshots(t *testing.T) {
	game, dir := os.Getenv("SWTR_SHOT_GAME"), os.Getenv("SWTR_SHOT_DIR")
	if game == "" || dir == "" {
		t.Skip("SWTR_SHOT_GAME / SWTR_SHOT_DIR не заданы")
	}
	keysPath := os.Getenv("SWTR_SHOT_KEYS")
	if keysPath == "" {
		home, _ := os.UserHomeDir()
		keysPath = filepath.Join(home, ".switch", "prod.keys")
	}
	ks, err := keys.Load(keysPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name    string
		variant fyne.ThemeVariant
	}{{"dark", theme.VariantDark}, {"light", theme.VariantLight}} {
		a := test.NewApp()
		a.Settings().SetTheme(stTheme{variant: v.variant, fixed: true})
		u := &ui{app: a, prefs: a.Preferences(), logView: newLogView()}
		u.nowPlaying = -1
		u.win = test.NewWindow(nil)
		u.win.Resize(fyne.NewSize(1200, 820))
		if os.Getenv("SWTR_SHOT_SCALE") == "2" { // снимки вдвое чётче — для роликов
			if c, ok := u.win.Canvas().(test.WindowlessCanvas); ok {
				c.SetScale(2)
			}
		}
		u.prefs.SetString("from", "en")               // английская версия текста и озвучки, как у Ring Fit по умолчанию
		if v := os.Getenv("SWTR_SHOT_TTS"); v != "" { // какая озвучка выбрана на вкладке «Озвучка»
			u.prefs.SetString("ttsProvider", v)
		}
		if l := os.Getenv("SWTR_SHOT_LANG"); l != "" { // язык интерфейса
			i18n.Set(l)
		}
		u.build()
		work := t.TempDir()
		// SWTR_SHOT_WORK — папка прошлой работы: оттуда берутся распознанная речь и перевод (копией, оригинал не трогаем)
		if from := os.Getenv("SWTR_SHOT_WORK"); from != "" {
			entries, _ := os.ReadDir(from) // не Glob: в именах папок с играми бывают [скобки]
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					if b, err := os.ReadFile(filepath.Join(from, e.Name())); err == nil {
						os.WriteFile(filepath.Join(work, e.Name()), b, 0o644)
					}
				}
			}
		}
		s, err := project.Analyze(game, project.FindExtras(game), ks, work, func(string, ...any) {}, func(string, float64, float64) {})
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("SWTR_SHOT_VOICES") != "" {
			// реплики «разделены по голосам»: три голоса вперемешку
			s.SelectLocale("USen")
			n := 0
			for i := range s.Audio {
				if s.Audio[i].Transcript != "" {
					s.Audio[i].Speaker = []string{"Ринг", "Голос 2", "Голос 3", "Ринг"}[n%4]
					n++
				}
			}
		}
		u.session = s
		u.setGame(game)
		u.showReport()
		for i := 0; i < len(u.tabs.Items); i++ {
			u.tabs.SelectIndex(i)
			if i == 2 && os.Getenv("SWTR_SHOT_GROUP") != "" {
				s.SelectLocale("USen")
				u.fillAudio()
				for _, o := range u.audioGroup.Options {
					if strings.Contains(o, os.Getenv("SWTR_SHOT_GROUP")) {
						u.audioGroup.SetSelected(o)
						break
					}
				}
				u.audioKind.SetSelected("Похоже на голос")
			}
			if i == 2 {
				// правка звука: первая строка с распознанной речью
				for row, k := range u.audioView {
					if s.Audio[k].Transcript != "" {
						u.audioListW.Select(row)
						break
					}
				}
			}
			if i == 2 && os.Getenv("SWTR_SHOT_PLAY") != "" && len(u.audioView) > 1 {
				// показать плеер: беззвучный файл «играет» во второй строке
				wav := filepath.Join(t.TempDir(), "silence.wav")
				os.WriteFile(wav, audio.WAV(&audio.PCM{Rate: 16000, Channels: [][]int16{make([]int16, 16000*40)}}), 0o644)
				speech.PlayFrom(wav, 12, nil)
				u.nowPlaying = u.audioView[1]
				u.audioListW.RefreshItem(1)
				defer speech.StopPlayback()
			}
			if i == 3 {
				u.collectGlossary() // словарь — и его окно
				t.Logf("словарь: %d терминов", len(s.Glossary))
				if u.glossaryWin != nil {
					u.glossaryWin.Resize(fyne.NewSize(900, 640))
					g, _ := os.Create(filepath.Join(dir, v.name+"-glossary.png"))
					png.Encode(g, u.glossaryWin.Canvas().Capture())
					g.Close()
					u.glossaryWin.Close()
				}
			}
			f, err := os.Create(filepath.Join(dir, v.name+"-tab"+string(rune('1'+i))+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, u.win.Canvas().Capture())
			f.Close()
			if i == 4 && os.Getenv("SWTR_SHOT_VOICES") != "" {
				u.speakersDialog()
				f, _ := os.Create(filepath.Join(dir, v.name+"-voices.png"))
				png.Encode(f, u.win.Canvas().Capture())
				f.Close()
			}
		}
		s.Close()
	}
}
