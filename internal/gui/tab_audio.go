package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"switch-translator/internal/i18n"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/nx"
	"switch-translator/internal/project"
	"switch-translator/internal/speech"
)

// audioTab — звук игры: послушать, отметить, распознать речь (вкладка «Речь в текст») и переозвучить
// перевод (вкладка «Озвучка»). Список звука один, он переезжает между этими вкладками.
type audioTab struct {
	decoderBox *fyne.Container
	sttSel     *widget.Select
	sttKey     *widget.Entry
	sttModel   *widget.SelectEntry
	sttHelp    *fyne.Container
	sttStatus  *widget.RichText
	modelBtn   *widget.Button

	ttsSel     *widget.Select
	ttsKey     *widget.Entry
	ttsVoice   *widget.SelectEntry
	ttsRegion  *widget.SelectEntry // регион Azure
	ttsStyle   *widget.Entry       // как говорить (Gemini)
	ttsStatus  *widget.RichText    // готов ли локальный голос
	setupBtn   *widget.Button      // скачать или установить локальный голос
	ttsHelp    *fyne.Container
	voiceBtns  *fyne.Container
	previewBtn *widget.Button
	reloadBtn  *widget.Button
	cloneBtn   *widget.Button
	elVoices   []speech.Voice
	ttsVoices  []string // голоса в списке «Голос»

	perVoiceChk *widget.Check   // каждому персонажу — свой голос
	speakersBtn *widget.Button  // окно голосов персонажей
	voicesBox   *fyne.Container // раздел «Персонажи» — когда реплики разделены по голосам

	audioListW   *widget.List
	audioCounter *widget.Label
	audioActs    []*widget.Button
	nowPlaying   int  // какой файл играет (индекс в Audio), -1 — ничего
	nowDub       bool // играет озвучка, а не оригинал
	seeking      bool // полосу перематывают — не дёргать её таймером
	playTicker   *time.Ticker
	audioSearch  *widget.Entry
	audioGroup   *widget.Select
	audioGroups  []audioGroup
	audioKind    *widget.Select
	lastChecked  int // строка, где галочку нажали последней (для Shift+клик)
	groupsFor    *project.Session
	groupsLen    int
	audioView    []int // индексы файлов, подходящих под поиск
}

func (u *ui) audioButtons() []*widget.Button { return u.audioActs }

// buildAudioTab строит список звука (u.audioPane) и левые панели двух этапов: распознавание и озвучку.
func (u *ui) buildAudioTab() (recognizeSide, dubSide []fyne.CanvasObject) {
	// --- декодер ---
	u.decoderBox = container.NewVBox()
	u.updateDecoder()

	// --- распознавание ---
	sttOpts := speech.STTOptions()
	sttLabels := speechLabels(sttOpts)
	u.sttKey = widget.NewPasswordEntry()
	u.sttKey.SetPlaceHolder(T("вставь ключ сюда"))
	u.sttModel = widget.NewSelectEntry(nil)
	u.sttModel.OnChanged = func(string) { u.updateWhisperStatus() }
	u.sttHelp = container.NewVBox()
	u.sttStatus = widget.NewRichText()
	u.sttStatus.Wrapping = fyne.TextWrapWord
	u.modelBtn = widget.NewButtonWithIcon(T("Скачать модель"), theme.DownloadIcon(), u.downloadModel)
	u.sttSel = newSelect(sttLabels, func(sel string) { u.onSTT(sttOpts[indexOf(sttLabels, sel)]) })

	// --- озвучка ---
	ttsOpts := speech.TTSOptions()
	ttsLabels := speechLabels(ttsOpts)
	u.ttsKey = widget.NewPasswordEntry()
	u.ttsKey.SetPlaceHolder(T("вставь ключ сюда"))
	u.ttsKey.OnSubmitted = func(string) { u.reloadVoices() }
	u.ttsVoice = widget.NewSelectEntry(nil)
	u.ttsVoice.OnChanged = func(string) { u.updateTTSStatus() } // у Piper каждый голос скачивается отдельно
	u.ttsRegion = widget.NewSelectEntry(speech.AzureRegions)
	u.ttsRegion.SetPlaceHolder(T("например, westeurope"))
	u.ttsRegion.SetText(u.prefs.String("region.azure"))
	u.ttsRegion.OnChanged = func(r string) { u.prefs.SetString("region.azure", strings.TrimSpace(r)) }
	u.ttsRegion.OnSubmitted = func(string) { u.reloadVoices() }
	u.ttsStyle = widget.NewEntry()
	u.ttsStyle.SetPlaceHolder(T("например: бодро и дружелюбно, как тренер в фитнес-игре"))
	u.ttsStyle.OnChanged = func(v string) { u.prefs.SetString("style."+u.currentTTS().ID, v) }
	u.ttsStatus = widget.NewRichText()
	u.ttsStatus.Wrapping = fyne.TextWrapWord
	u.setupBtn = widget.NewButtonWithIcon("", theme.DownloadIcon(), u.setupVoice)
	u.ttsHelp = container.NewVBox()
	u.previewBtn = widget.NewButtonWithIcon(T("Прослушать голос"), theme.MediaPlayIcon(), u.previewVoice)
	u.reloadBtn = widget.NewButtonWithIcon(T("Обновить список"), theme.ViewRefreshIcon(), u.reloadVoices)
	u.cloneBtn = widget.NewButtonWithIcon(T("Скопировать голос из отмеченных"), theme.ContentCopyIcon(), u.cloneVoice)
	u.voiceBtns = container.NewHBox(u.previewBtn, u.reloadBtn)
	u.ttsSel = newSelect(ttsLabels, func(sel string) { u.onTTS(ttsOpts[indexOf(ttsLabels, sel)]) })

	guess := widget.NewButtonWithIcon(T("Найти голос — быстро, без модели"), theme.SearchIcon(), u.guessVoices)
	recognize := widget.NewButtonWithIcon(T("Распознать речь в звуке"), theme.SearchIcon(), u.recognize)
	recognize.Importance = widget.HighImportance
	dub := widget.NewButtonWithIcon(T("Озвучить перевод речи"), theme.VolumeUpIcon(), u.dub)
	dub.Importance = widget.HighImportance
	split := widget.NewButtonWithIcon(T("Разделить по голосам"), theme.AccountIcon(), u.splitVoices)
	u.speakersBtn = widget.NewButtonWithIcon(T("Голоса персонажей…"), theme.AccountIcon(), u.speakersDialog)
	u.perVoiceChk = widget.NewCheck(T("Каждому персонажу — свой голос"), func(on bool) { u.prefs.SetBool("perVoice", on) })
	u.perVoiceChk.SetChecked(u.prefs.BoolWithFallback("perVoice", true))
	u.voicesBox = container.NewVBox(
		section(T("Персонажи")),
		u.perVoiceChk,
		muted(T("Реплики разделены по голосам. С галочкой каждый голос озвучивается своим голосом: выбранным в окне ")+
			T("«Голоса персонажей» или, если озвучка умеет копировать голос (XTTS, ElevenLabs, Cartesia), скопированным ")+
			T("по репликам этого персонажа в оригинале. Реплики без голоса — голосом, выбранным выше.")),
		container.NewHBox(u.speakersBtn),
		widget.NewSeparator(),
	)
	u.voicesBox.Hide()

	sttForm := newForm(T("Чем распознавать"), u.sttSel, T("Ключ"), u.sttKey, T("Модель"), u.sttModel)
	ttsForm := newForm(T("Чем озвучивать"), u.ttsSel, T("Ключ"), u.ttsKey, T("Регион"), u.ttsRegion, T("Голос"), u.ttsVoice, T("Как говорить"), u.ttsStyle)
	recognizeSide = []fyne.CanvasObject{
		u.decoderBox,
		section(T("Распознавание")),
		sttForm, u.sttStatus, container.NewHBox(u.modelBtn), u.sttHelp,
		widget.NewSeparator(),
		section(T("Звук игры")),
		muted(T("Послушай файлы справа и отметь те, где есть речь. Шаги работают только с отмеченными файлами. ") +
			T("Щелчок по строке открывает её правку: свой текст и перевод, запись озвучки с микрофона или свой файл.")),
		step(guess, T("За секунды прикидывает по звуку, где голос, а где музыка и эффекты, и отмечает файлы с голосом. ")+
			T("Это подсказка: голос без слов (вскрики, смех) тоже попадёт в отмеченные, а редкую реплику на фоне музыки можно пропустить.")),
		step(recognize, T("Находит речь и записывает её текстом. В музыке и эффектах речи не будет.")),
		step(split, T("Делит отмеченные реплики с речью по голосам: «Голос 1», «Голос 2»… — первый у того, кто говорит больше всех. ")+
			T("Голос можно переименовать в персонажа, а у отдельной реплики поправить руками. На «Озвучке» каждый голос ")+
			T("можно озвучить своим голосом — например, скопированным по его же репликам.")),
	}
	dubSide = []fyne.CanvasObject{
		section(T("Голос")),
		ttsForm, u.ttsStatus, container.NewHBox(u.setupBtn), u.voiceBtns, u.ttsHelp,
		widget.NewSeparator(),
		u.voicesBox, // голоса персонажей решают, чем озвучивать, — до кнопки озвучки
		section(T("Звук игры")),
		step(dub, T("Озвучивает переведённую речь в отмеченных файлах выбранным голосом — или голосами персонажей, если стоит галочка выше. ")+
			T("Готовое можно послушать справа кнопкой «Перевод».")),
	}

	// --- список файлов ---
	u.audioCounter = widget.NewLabel("")
	selAll := widget.NewButtonWithIcon(T("Отметить найденные"), theme.CheckButtonCheckedIcon(), func() { u.setFound(true, false) })
	selNone := widget.NewButtonWithIcon(T("Снять найденные"), theme.CheckButtonIcon(), func() { u.setFound(false, false) })
	invert := widget.NewButtonWithIcon(T("Обратить"), theme.ViewRefreshIcon(), func() { u.setFound(false, true) })
	stop := widget.NewButtonWithIcon(T("Стоп"), theme.MediaStopIcon(), u.stopPlaying)
	for _, b := range []*widget.Button{selAll, selNone, invert, stop} {
		b.Importance = widget.LowImportance
	}
	u.audioActs = []*widget.Button{guess, recognize, split, dub, u.cloneBtn, u.speakersBtn, selAll, selNone, invert}
	editor := u.buildAudioEditor()
	u.audioActs = append(u.audioActs, u.aeBtns...)
	u.audioSearch = widget.NewEntry()
	u.audioSearch.SetPlaceHolder(T("Поиск: имя файла, распознанный текст или перевод"))
	u.audioSearch.OnChanged = func(string) { u.fillAudio() }
	u.audioGroup = newSelect([]string{allGroups()}, func(string) { u.fillAudio() })
	u.audioGroup.Selected = allGroups()
	u.audioKind = newKindSelect(u.fillAudio)
	u.lastChecked = -1
	filters := container.NewGridWithColumns(2, u.audioGroup, u.audioKind)
	head := container.NewVBox(
		muted(T("▶ — послушать. Выбери архив или папку и вид звука, затем «Отметить найденные». ")+
			T("Shift+клик по галочке отмечает или снимает сразу всё от прошлой нажатой галочки.")),
		filters,
		u.audioSearch,
		u.audioCounter,
		container.NewHBox(selAll, selNone, invert, stop),
	)
	u.audioListW = widget.NewList(
		func() int { return len(u.audioView) },
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			play := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), nil)
			play.Importance = widget.LowImportance
			stop := widget.NewButtonWithIcon("", theme.MediaStopIcon(), nil)
			stop.Importance = widget.LowImportance
			playDub := widget.NewButtonWithIcon(T("Перевод"), theme.MediaPlayIcon(), nil)
			playDub.Importance = widget.LowImportance
			name := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			name.Truncation = fyne.TextTruncateEllipsis
			info := widget.NewRichText(&widget.TextSegment{Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder}})
			info.Truncation = fyne.TextTruncateEllipsis
			// полоса перемотки — только у того, что играет
			seek := widget.NewSlider(0, 1)
			seek.Step = 0.01
			clock := canvas.NewText("", theme.Color(theme.ColorNamePlaceHolder))
			clock.TextSize = theme.CaptionTextSize()
			bar := container.NewBorder(nil, nil, nil, clock, seek)
			text := container.New(tightVBox(), name, info, bar)
			return container.NewBorder(nil, nil, container.NewHBox(chk, play, stop), playDub, text)
		},
		func(row widget.ListItemID, o fyne.CanvasObject) {
			if row >= len(u.audioView) {
				return
			}
			i := u.audioView[row]
			l := u.session.Audio[i]
			c := o.(*fyne.Container)
			text := c.Objects[0].(*fyne.Container)
			left := c.Objects[1].(*fyne.Container)
			playDub := c.Objects[2].(*widget.Button)
			chk, play, stop := left.Objects[0].(*widget.Check), left.Objects[1].(*widget.Button), left.Objects[2].(*widget.Button)
			chk.OnChanged = nil
			chk.SetChecked(l.Selected)
			chk.OnChanged = func(on bool) { u.checkRow(row, on) }
			text.Objects[0].(*widget.Label).SetText(fmt.Sprintf("%s · %s", audioName(l.File), nx.HumanSize(l.Size)))
			info := text.Objects[1].(*widget.RichText)
			note := audioInfo(l.Recognized, l.Transcript, l.Translation, l.Error) + speechGuess(l)
			if strings.HasPrefix(l.Format, "начало музыки") {
				note = i18n.Phrases(l.Format) + " · " + note
			}
			if l.Speaker != "" {
				note = l.Speaker + " · " + note
			}
			info.Segments[0].(*widget.TextSegment).Text = note
			info.Refresh()

			cur := u.nowPlaying == i
			st := speech.State()
			if cur && st.Path == "" {
				cur = false // доиграло, таймер ещё не успел заметить
			}
			icon := func(dub bool) fyne.Resource {
				if cur && u.nowDub == dub && !st.Paused {
					return theme.MediaPauseIcon()
				}
				return theme.MediaPlayIcon()
			}
			play.SetIcon(icon(false))
			play.OnTapped = func() { u.togglePlay(i, false) }
			playDub.SetIcon(icon(true))
			playDub.OnTapped = func() { u.togglePlay(i, true) }
			setVisible(playDub, l.Dub != "")
			setVisible(stop, cur)
			stop.OnTapped = u.stopPlaying

			bar := text.Objects[2].(*fyne.Container)
			seek, clock := bar.Objects[0].(*widget.Slider), bar.Objects[1].(*canvas.Text)
			setVisible(bar, cur && st.Dur > 0)
			if cur && st.Dur > 0 && !u.seeking {
				seek.OnChanged, seek.OnChangeEnded = nil, nil
				seek.Max = st.Dur
				seek.SetValue(st.Pos)
				clock.Text = mmss(st.Pos) + " / " + mmss(st.Dur)
				clock.Refresh()
				seek.OnChanged = func(v float64) {
					u.seeking = true
					clock.Text = mmss(v) + " / " + mmss(st.Dur)
					clock.Refresh()
				}
				seek.OnChangeEnded = func(v float64) {
					u.seeking = false
					if err := speech.Seek(v); err != nil {
						u.showError(err)
					}
				}
			}
		})

	u.audioListW.OnSelected = func(row widget.ListItemID) {
		if row < len(u.audioView) {
			u.openAudioEditor(u.audioView[row])
		}
	}
	u.audioPane = container.NewBorder(head, editor, nil, nil, u.audioListW)

	u.sttSel.SetSelectedIndex(max(0, speechIndex(sttOpts, u.prefs.String("sttProvider"))))
	u.ttsSel.SetSelectedIndex(max(0, speechIndex(ttsOpts, u.prefs.String("ttsProvider"))))
	return recognizeSide, dubSide
}

// step — кнопка шага и пояснение под ней.
func step(btn *widget.Button, text string) fyne.CanvasObject {
	return container.NewVBox(container.NewHBox(btn), muted(text))
}

// tightVBox — строки одной мысли (имя файла и что в нём) без большого зазора.
func tightVBox() fyne.Layout { return layout.NewCustomPaddedVBoxLayout(-theme.Padding() * 2) }

// audioName — имя для списка; звук из архива BARS: «PreviewVoice_USen — в VoicePreview.bars».
func audioName(file string) string {
	if archive, name, ok := strings.Cut(file, "#"); ok {
		return name + T(" — в ") + path.Base(archive)
	}
	return path.Base(file)
}

func audioInfo(recognized bool, transcript, translation, errText string) string {
	switch {
	case errText != "":
		return T("Ошибка: ") + errText
	case !recognized:
		return T("ещё не распознан")
	case transcript == "":
		return T("речи нет — музыка или эффекты")
	case translation != "":
		return "«" + oneLine(transcript) + "» → «" + oneLine(translation) + "»"
	default:
		return T("речь: «") + oneLine(transcript) + "»"
	}
}

func speechLabels(opts []speech.Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label()
	}
	return out
}

// updateDecoder предупреждает, если в игре есть звук не от Nintendo, а vgmstream на Mac не установлен.
// Форматы Nintendo программа читает сама, под Windows и Linux vgmstream скачивается сам.
func (u *ui) updateDecoder() {
	u.decoderBox.Objects = nil
	foreign := 0
	if u.session != nil {
		for _, l := range u.session.Audio {
			if !speech.NativeFormat(l.Format) && !speech.FFmpegFormat(l.Format) {
				foreign++
			}
		}
	}
	if foreign > 0 && runtime.GOOS == "darwin" && !speech.HaveVgmstream() {
		st := widget.NewRichText()
		st.Wrapping = fyne.TextWrapWord
		richStatus(st, false, fmt.Sprintf(T("%d файлов в формате, который программа сама не читает (не Nintendo и не по зубам встроенному ffmpeg)."), foreign),
			T("Чтобы слушать их, установи vgmstream:"))
		u.decoderBox.Objects = []fyne.CanvasObject{st, u.commandRow(speech.VgmstreamCommand)}
	}
	u.decoderBox.Refresh()
}

func (u *ui) fillAudio() {
	u.updateDecoder()
	u.updateMediaTabs()
	// группы — заново, только когда сменилась игра или список (другой язык озвучки)
	if u.session != u.groupsFor || (u.session != nil && len(u.session.Audio) != u.groupsLen) {
		u.groupsFor = u.session
		if u.session != nil {
			u.groupsLen = len(u.session.Audio)
		}
		u.buildGroups()
	}
	u.syncAudioEditor()
	u.updateSpeakers()
	u.filterAudio()
	u.audioListW.Refresh()
	u.updateAudioCounter()
}

// filterAudio — какие файлы показать: все слова поиска есть в имени файла, расшифровке или переводе.
func (u *ui) filterAudio() {
	u.audioView = u.audioView[:0]
	if u.session == nil {
		return
	}
	words := strings.Fields(strings.ToLower(u.audioSearch.Text))
	prefix := u.groupPrefix()
next:
	for i, l := range u.session.Audio {
		if !inGroup(l.File, prefix) || !u.kindMatch(l) {
			continue
		}
		hay := strings.ToLower(l.File + " " + l.Transcript + " " + l.Translation + " " + l.Guess + " " + l.Speaker)
		for _, w := range words {
			if !strings.Contains(hay, w) {
				continue next
			}
		}
		u.audioView = append(u.audioView, i)
	}
}

func (u *ui) updateAudioCounter() {
	if u.session == nil {
		return
	}
	sel, recognized, speechN, guessed := 0, 0, 0, 0
	for _, l := range u.session.Audio {
		if !l.Recognized && l.Guess == "голос" {
			guessed++
		}
		if l.Selected {
			sel++
		}
		if l.Recognized {
			recognized++
			if l.Transcript != "" {
				speechN++
			}
		}
	}
	text := fmt.Sprintf(T("Файлов: %d · отмечено %d"), len(u.session.Audio), sel)
	if len(u.audioView) != len(u.session.Audio) {
		text = fmt.Sprintf(T("Найдено %d из %d · отмечено всего %d"), len(u.audioView), len(u.session.Audio), sel)
	}
	if recognized > 0 {
		text += fmt.Sprintf(T(" · распознано %d, с речью %d"), recognized, speechN)
	}
	if guessed > 0 {
		text += fmt.Sprintf(T(" · похоже на голос ещё %d"), guessed)
	}
	u.audioCounter.SetText(text)
}

// markAudio отмечает файлы по условию; speech — распознан и в нём есть речь.
func (u *ui) markAudio(keep func(speech bool) bool) {
	if u.session == nil {
		return
	}
	// только среди найденного: «найти VO_ → Отметить все» отмечает одни голоса
	for _, i := range u.audioView {
		l := &u.session.Audio[i]
		// речь: распознана, а если ещё не распознавали — по быстрой подсказке
		l.Selected = keep(l.Recognized && l.Transcript != "" || !l.Recognized && l.Guess == "голос")
	}
	u.session.SaveAudio()
	u.audioListW.Refresh()
	u.updateAudioCounter()
}

func (u *ui) selectedAudio() []int {
	var idx []int
	for i, l := range u.session.Audio {
		if l.Selected {
			idx = append(idx, i)
		}
	}
	return idx
}

// ---------- прослушивание ----------

func (u *ui) play(file string) {
	if err := speech.Play(file, nil); err != nil {
		u.showError(err)
	}
}

// togglePlay — ▶ у файла: играет — пауза, на паузе — дальше, иначе — сначала (оригинал или озвучку).
func (u *ui) togglePlay(i int, dub bool) {
	if u.nowPlaying == i && u.nowDub == dub && speech.State().Path != "" {
		if _, ok := speech.PausePlayback(); !ok {
			u.stopPlaying() // где паузы нет (Windows) — кнопка останавливает
		}
		u.refreshPlaying()
		return
	}
	start := func(path string) {
		prev := u.nowPlaying
		err := speech.Play(path, func() { fyne.Do(func() { u.playEnded(i) }) })
		if err != nil {
			u.showError(err)
			return
		}
		u.nowPlaying, u.nowDub = i, dub
		u.refreshRow(prev)
		u.refreshPlaying()
		u.startPlayTicker()
	}
	if dub {
		if u.fitDubs == nil || !u.fitDubs.Checked {
			start(u.session.Audio[i].Dub)
			return
		}
		// так, как прозвучит в игре: с подгонкой по длине
		s := u.session
		u.goSafe(func() {
			wav, _, err := s.FittedDubWav(context.Background(), i)
			fyne.Do(func() {
				if err != nil {
					wav = s.Audio[i].Dub
				}
				start(wav)
			})
		})
		return
	}
	file := u.session.Audio[i].File
	u.goSafe(func() {
		wav, err := u.session.AudioWav(context.Background(), file)
		fyne.Do(func() {
			if err != nil {
				u.showError(err)
				return
			}
			start(wav)
		})
	})
}

func (u *ui) stopPlaying() {
	speech.StopPlayback()
	prev := u.nowPlaying
	u.nowPlaying = -1
	u.refreshRow(prev)
}

func (u *ui) playEnded(i int) {
	if u.nowPlaying == i && speech.State().Path == "" {
		u.nowPlaying = -1
		u.refreshRow(i)
	}
}

// refreshRow перерисовывает строку файла i, если она сейчас видна в списке.
func (u *ui) refreshRow(i int) {
	if i < 0 || u.audioListW == nil {
		return
	}
	for row, j := range u.audioView {
		if j == i {
			u.audioListW.RefreshItem(row)
			return
		}
	}
}

func (u *ui) refreshPlaying() { u.refreshRow(u.nowPlaying) }

// startPlayTicker двигает полосу перемотки, пока что-то играет.
func (u *ui) startPlayTicker() {
	if u.playTicker != nil {
		return
	}
	u.playTicker = time.NewTicker(250 * time.Millisecond)
	go func() {
		for range u.playTicker.C {
			fyne.Do(func() {
				if u.nowPlaying < 0 {
					return
				}
				if !u.seeking && !speech.State().Paused {
					u.refreshPlaying()
				}
			})
		}
	}()
}

func mmss(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// ---------- распознавание ----------

func (u *ui) onSTT(o speech.Option) {
	u.prefs.SetString("sttProvider", o.ID)
	u.sttKey.SetText(u.prefs.String("apikey." + vendor(o.ID)))
	setVisible(u.sttKey, o.NeedsKey)
	models := o.Models
	if o.ID == "whispercpp" {
		models = nil
		for _, m := range speech.WhisperModels {
			models = append(models, m.Label())
		}
	}
	u.sttModel.SetOptions(models)
	m := u.prefs.String("model." + o.ID)
	if m == "" || (o.ID == "whispercpp" && strings.Contains(m, "/")) {
		m = ""
		if o.ID == "whispercpp" {
			m = speech.WhisperModels[2].Label() // large-v3-turbo
		} else if len(models) > 0 {
			m = models[0]
		}
	}
	u.sttModel.SetText(m)
	setVisible(u.sttModel, len(models) > 0)
	open := o.NeedsKey && u.sttKey.Text == "" || o.ID == "whispercpp" && !fileExists(speech.WhisperModelPath(m))
	setHelp(u.sttHelp, T("Как подключить «")+T(o.Title)+"»", u.helpContent(o.Site, o.Help, o.Commands), open)
	u.updateWhisperStatus()
}

func (u *ui) currentSTT() speech.Option {
	opts := speech.STTOptions()
	return opts[speechIndexByLabel(opts, u.sttSel.Selected)]
}

func (u *ui) updateWhisperStatus() {
	if u.sttStatus == nil || u.sttSel == nil || u.sttSel.Selected == "" {
		return
	}
	if u.currentSTT().ID != "whispercpp" {
		u.sttStatus.Hide()
		u.modelBtn.Hide()
		return
	}
	u.sttStatus.Show()
	model := speech.WhisperModelPath(u.sttModel.Text)
	switch {
	case fileExists(model):
		richStatus(u.sttStatus, true, T("Готово к работе"), T("Whisper встроен в программу, модель: ")+filepath.Base(model))
	default:
		richStatus(u.sttStatus, false, T("Модель ещё не скачана — нажми «Скачать модель»."), "")
	}
	setVisible(u.modelBtn, !fileExists(model))
}

func (u *ui) downloadModel() {
	name := u.sttModel.Text
	u.run(T("Скачивание модели Whisper"), true, func(ctx context.Context) (func(), error) {
		err := speech.DownloadWhisperModel(ctx, name, func(done, total int64) {
			u.progressFn(T("Скачивание модели"), float64(done), float64(total))
		})
		if err != nil {
			return nil, err
		}
		u.logf("Модель скачана: %s", speech.WhisperModelPath(name))
		return u.updateWhisperStatus, nil
	})
}

func (u *ui) recognizer() (speech.Recognizer, error) {
	o := u.currentSTT()
	cfg := speech.Config{APIKey: strings.TrimSpace(u.sttKey.Text), Model: strings.TrimSpace(u.sttModel.Text), Lang: u.fromLang()}
	if cfg.APIKey != "" {
		u.prefs.SetString("apikey."+vendor(o.ID), cfg.APIKey)
	}
	u.prefs.SetString("model."+o.ID, cfg.Model)
	return speech.NewRecognizer(o.ID, cfg)
}

func (u *ui) needSelected() []int {
	if u.session == nil {
		return nil
	}
	idx := u.selectedAudio()
	if len(idx) == 0 {
		dialog.ShowInformation(T("Ничего не отмечено"), T("Отметь галочками файлы справа — шаг работает только с ними."), u.win)
	}
	return idx
}

func (u *ui) recognize() {
	idx := u.needSelected()
	if len(idx) == 0 {
		return
	}
	stt, err := u.recognizer()
	if err != nil {
		u.showError(err)
		return
	}
	s := u.session
	u.run(T("Распознавание речи"), true, func(ctx context.Context) (func(), error) {
		err := s.Recognize(ctx, stt, idx, u.progressFn)
		return u.fillAudio, err
	})
}

// ---------- озвучка ----------

func (u *ui) currentTTS() speech.Option {
	opts := speech.TTSOptions()
	return opts[speechIndexByLabel(opts, u.ttsSel.Selected)]
}

func (u *ui) onTTS(o speech.Option) {
	u.prefs.SetString("ttsProvider", o.ID)
	u.ttsKey.SetText(u.prefs.String("apikey." + vendor(o.ID)))
	setVisible(u.ttsKey, o.NeedsKey)
	setVisible(u.ttsRegion, o.ID == "azure")
	setVisible(u.ttsStyle, o.Style)
	u.ttsStyle.SetText(u.prefs.String("style." + o.ID))
	setVisible(u.ttsVoice, o.ID != "none")
	setVisible(u.voiceBtns, o.ID != "none")
	u.voiceBtns.Objects = []fyne.CanvasObject{u.previewBtn, u.reloadBtn}
	if o.Clone {
		u.voiceBtns.Objects = append(u.voiceBtns.Objects, u.cloneBtn)
	}
	u.voiceBtns.Refresh()
	u.ttsVoice.SetText(u.prefs.String("model." + o.ID))
	var content fyne.CanvasObject
	if o.Help != "" || o.Site != "" {
		content = u.helpContent(o.Site, o.Help, o.Commands)
	}
	open := o.NeedsKey && u.ttsKey.Text == ""
	if o.Setup != "" {
		ready, _ := speech.Ready(o.ID, u.ttsConfig())
		open = open || !ready
	}
	setHelp(u.ttsHelp, T("Как подключить «")+T(o.Title)+"»", content, open)
	u.reloadVoices()
	u.updateTTSStatus()
}

// ttsConfig — настройки озвучки из полей вкладки.
func (u *ui) ttsConfig() speech.Config {
	return speech.Config{APIKey: strings.TrimSpace(u.ttsKey.Text), Model: strings.TrimSpace(u.ttsVoice.Text), Lang: u.toLang(),
		Region: strings.TrimSpace(u.ttsRegion.Text), Style: strings.TrimSpace(u.ttsStyle.Text)}
}

// updateTTSStatus — готов ли локальный голос и что для этого скачать или установить.
func (u *ui) updateTTSStatus() {
	if u.ttsStatus == nil || u.ttsSel == nil || u.ttsSel.Selected == "" {
		return
	}
	o := u.currentTTS()
	if o.Setup == "" {
		u.ttsStatus.Hide()
		u.setupBtn.Hide()
		return
	}
	ready, text := speech.Ready(o.ID, u.ttsConfig())
	if ready {
		richStatus(u.ttsStatus, true, T("Готово к работе"), text)
	} else {
		richStatus(u.ttsStatus, false, text, "")
	}
	u.ttsStatus.Show()
	u.setupBtn.SetText(T(o.Setup))
	setVisible(u.setupBtn, !ready)
}

// setupVoice скачивает или ставит то, что нужно локальному голосу (Piper, Silero, XTTS).
func (u *ui) setupVoice() {
	o := u.currentTTS()
	cfg := u.ttsConfig()
	u.run(T(o.Setup), true, func(ctx context.Context) (func(), error) {
		err := speech.Setup(ctx, o.ID, cfg, func(line string) { u.logf("%s", line) }, func(done, total int64) {
			u.progressFn(T(o.Setup), float64(done), float64(total))
		})
		if err != nil {
			return u.updateTTSStatus, err
		}
		u.logf("%s: готово", o.Title)
		return func() {
			u.updateTTSStatus()
			u.reloadVoices()
		}, nil
	})
}

// reloadVoices заполняет список голосов: голоса macOS для языка перевода, голоса OpenAI или аккаунта ElevenLabs.
func (u *ui) reloadVoices() {
	o := u.currentTTS()
	lang := u.toLang()
	set := func(voices []string) {
		u.ttsVoices = voices
		u.ttsVoice.SetOptions(voices)
		if u.ttsVoice.Text == "" && len(voices) > 0 {
			u.ttsVoice.SetText(voices[0])
		}
	}
	switch o.ID {
	case "say":
		voices := speech.SayVoices(lang)
		set(voices)
		if len(voices) == 0 {
			u.ttsVoice.SetPlaceHolder(T("для языка «") + lang + T("» голосов нет — см. инструкцию"))
		}
	case "openai-tts":
		set(speech.OpenAIVoices())
	case "windows":
		voices := speech.WindowsVoices(lang)
		set(voices)
		if len(voices) == 0 {
			u.ttsVoice.SetPlaceHolder(T("для языка «") + lang + T("» голосов нет — см. инструкцию"))
		}
	case "azure":
		key, region := strings.TrimSpace(u.ttsKey.Text), strings.TrimSpace(u.ttsRegion.Text)
		if key == "" || region == "" {
			u.ttsVoice.SetPlaceHolder(T("впиши ключ и регион — список голосов загрузится"))
			return
		}
		u.prefs.SetString("apikey.azure", key)
		u.goSafe(func() {
			voices, err := speech.AzureVoices(context.Background(), key, region, lang)
			fyne.Do(func() {
				if err != nil {
					u.logf("Azure: %v", err)
					return
				}
				set(voices)
			})
		})
	case "none":
	default:
		cfg := u.ttsConfig()
		if o.NeedsKey && cfg.APIKey == "" {
			u.ttsVoice.SetPlaceHolder(T("вставь ключ — список голосов загрузится"))
			return
		}
		if cfg.APIKey != "" {
			u.prefs.SetString("apikey."+vendor(o.ID), cfg.APIKey)
		}
		u.goSafe(func() {
			voices, err := speech.Voices(context.Background(), o.ID, cfg)
			fyne.Do(func() {
				if err != nil {
					u.logf("%s: %v", o.Title, err)
				}
				if len(voices) > 0 {
					set(voices)
				}
			})
		})
	case "elevenlabs":
		key := strings.TrimSpace(u.ttsKey.Text)
		if key == "" {
			u.ttsVoice.SetPlaceHolder(T("вставь ключ — список голосов загрузится"))
			return
		}
		u.prefs.SetString("apikey.elevenlabs", key)
		u.goSafe(func() {
			voices, err := speech.ElevenLabsVoices(context.Background(), key)
			fyne.Do(func() {
				if err != nil {
					u.logf("ElevenLabs: %v", err)
					return
				}
				u.elVoices = voices
				var labels []string
				for _, v := range voices {
					labels = append(labels, speech.VoiceLabel(v.Name, v.ID))
				}
				set(labels)
			})
		})
	}
}

func (u *ui) synthesizer() (speech.Synthesizer, error) {
	o := u.currentTTS()
	cfg := u.ttsConfig()
	if cfg.APIKey != "" {
		u.prefs.SetString("apikey."+vendor(o.ID), cfg.APIKey)
	}
	u.prefs.SetString("model."+o.ID, cfg.Model)
	return speech.NewSynthesizer(o.ID, cfg)
}

// previewVoice озвучивает короткую фразу выбранным голосом. У ElevenLabs есть готовые образцы — они бесплатные.
func (u *ui) previewVoice() {
	o := u.currentTTS()
	if o.ID == "elevenlabs" {
		id := speech.VoiceID(u.ttsVoice.Text)
		for _, v := range u.elVoices {
			if v.ID == id && v.PreviewURL != "" {
				u.goSafe(func() {
					dst := filepath.Join(os.TempDir(), "swtr-voice-"+id+".mp3")
					err := speech.Download(context.Background(), v.PreviewURL, dst)
					fyne.Do(func() {
						if err != nil {
							u.showError(err)
							return
						}
						u.play(dst)
					})
				})
				return
			}
		}
	}
	tts, err := u.synthesizer()
	if err != nil || tts == nil {
		if err == nil {
			err = errors.New(T("выбери, чем озвучивать"))
		}
		u.showError(err)
		return
	}
	// пробная фраза — в фоне, мимо общей полосы прогресса: она может быть занята скачиванием модели или переводом
	text := speech.Sample(u.toLang())
	u.previewBtn.Disable()
	u.goSafe(func() {
		out, err := tts.Synthesize(context.Background(), text, filepath.Join(os.TempDir(), "swtr-voice-preview"))
		fyne.Do(func() {
			u.previewBtn.Enable()
			if err != nil {
				u.showError(err)
				return
			}
			u.play(out)
		})
	})
}

// cloneVoice копирует голос персонажа по отмеченным репликам (ElevenLabs).
func (u *ui) cloneVoice() {
	idx := u.needSelected()
	if len(idx) == 0 {
		return
	}
	o := u.currentTTS()
	cfg := u.ttsConfig()
	if o.NeedsKey && cfg.APIKey == "" {
		dialog.ShowInformation(T("Нужен ключ ")+o.Title, T("Вставь API-ключ ")+o.Title+T(" в поле «Ключ»."), u.win)
		return
	}
	name := widget.NewEntry()
	name.SetText(T("Голос из игры"))
	dialog.ShowForm(T("Скопировать голос"), T("Скопировать"), T("Отмена"),
		[]*widget.FormItem{
			widget.NewFormItem(T("Название"), name),
			widget.NewFormItem("", muted(fmt.Sprintf(T("Образцы: %d отмеченных файлов. Лучше всего — 1–3 минуты чистой речи одного персонажа, без музыки."), len(idx)))),
		},
		func(ok bool) {
			if !ok {
				return
			}
			s := u.session
			u.run(T("Копирование голоса"), true, func(ctx context.Context) (func(), error) {
				var samples []string
				for _, i := range idx {
					wav, err := s.AudioWav(ctx, s.Audio[i].File)
					if err != nil {
						return nil, err
					}
					samples = append(samples, wav)
				}
				label, err := speech.CloneVoice(ctx, o.ID, cfg, name.Text, samples)
				if err != nil {
					return nil, err
				}
				u.logf("Голос «%s» создан (%s): %s", name.Text, o.Title, label)
				return func() {
					u.ttsVoice.SetText(label)
					u.reloadVoices()
				}, nil
			})
		}, u.win)
}

func (u *ui) dub() {
	idx := u.needSelected()
	if len(idx) == 0 {
		return
	}
	tts, err := u.synthesizer()
	if err != nil {
		u.showError(err)
		return
	}
	if tts == nil {
		dialog.ShowInformation(T("Озвучка выключена"), T("Выбери, чем озвучивать, в разделе «Голос»."), u.win)
		return
	}
	s := u.session
	start := func(idx []int) { u.startDub(tts, idx, u.fillAudio) }
	// уже озвученные (например, до остановки) не переозвучиваем без спроса
	var fresh []int
	done := 0
	for _, i := range idx {
		l := s.Audio[i]
		switch {
		case l.HandDub:
		case l.Dub != "" && fileExists(l.Dub):
			done++
		default:
			fresh = append(fresh, i)
		}
	}
	if done == 0 {
		start(idx)
		return
	}
	var d dialog.Dialog
	onlyNew := widget.NewButtonWithIcon(fmt.Sprintf(T("Только новые (%d)"), len(fresh)), theme.MediaPlayIcon(), func() {
		d.Hide()
		start(fresh)
	})
	onlyNew.Importance = widget.HighImportance
	if len(fresh) == 0 {
		onlyNew.Disable()
	}
	all := widget.NewButtonWithIcon(fmt.Sprintf(T("Все заново (%d)"), len(idx)), theme.ViewRefreshIcon(), func() {
		d.Hide()
		start(idx)
	})
	msg := widget.NewLabel(fmt.Sprintf(T("Из отмеченных %d файлов уже озвучено %d. Озвучить только остальные ")+
		T("или все заново — например, другим голосом?"), len(idx), done))
	msg.Wrapping = fyne.TextWrapWord
	d = dialog.NewCustom(T("Часть уже озвучена"), T("Отмена"), container.NewVBox(msg, container.NewHBox(onlyNew, all)), u.win)
	d.Resize(fyne.NewSize(520, 0))
	d.Show()
}

// speechGuess — догадка без Whisper, есть ли в файле голос (пока не распознан).
func speechGuess(l project.AudioLine) string {
	if l.Recognized || l.Guess == "" {
		return ""
	}
	return T(" · похоже: ") + l.Guess
}

// guessVoices — быстрый поиск голоса во всех найденных файлах, затем отмечаются файлы с голосом.
func (u *ui) guessVoices() {
	if u.session == nil {
		return
	}
	idx := append([]int(nil), u.audioView...)
	s := u.session
	u.run(T("Поиск голоса"), true, func(ctx context.Context) (func(), error) {
		err := s.GuessVoices(ctx, idx, u.progressFn)
		voices := 0
		for _, i := range idx {
			if s.Audio[i].Guess == "голос" {
				voices++
			}
		}
		u.logf("Похоже на голос: %d файлов из %d — они отмечены, остальное — музыка, эффекты и тишина", voices, len(idx))
		return func() { u.markAudio(func(speech bool) bool { return speech }) }, err
	})
}
