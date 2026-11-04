package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/speech"
)

// Правка одного звука: свой текст речи и перевод, распознать или перевести заново,
// озвучить голосом, записать с микрофона или взять готовый файл.

type audioEditor struct {
	aeIdx     int    // какой файл открыт (индекс в Audio), -1 — никакой
	aeFile    string // его имя: после смены языка озвучки индексы другие
	aeBox     *fyne.Container
	audioPane *fyne.Container
	aeTitle   *widget.Label
	aeText    *widget.Entry
	aeTransl  *widget.Entry
	aeDubInfo *widget.Label
	aeSpeaker *widget.SelectEntry // чей голос
	aePlayDub *widget.Button
	aeClear   *widget.Button
	aeBtns    []*widget.Button
}

func (u *ui) buildAudioEditor() fyne.CanvasObject {
	u.aeIdx = -1
	u.aeTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	u.aeTitle.Truncation = fyne.TextTruncateEllipsis
	u.aeText = widget.NewMultiLineEntry()
	u.aeText.Wrapping = fyne.TextWrapWord
	u.aeText.SetMinRowsVisible(2)
	u.aeText.SetPlaceHolder(T("Что говорят — впиши сам или нажми «Распознать». Пусто — речи нет."))
	u.aeTransl = widget.NewMultiLineEntry()
	u.aeTransl.Wrapping = fyne.TextWrapWord
	u.aeTransl.SetMinRowsVisible(2)
	u.aeTransl.SetPlaceHolder(T("Перевод — впиши сам или нажми «Перевести»"))
	u.aeDubInfo = widget.NewLabel("")
	u.aeSpeaker = widget.NewSelectEntry(nil)
	u.aeSpeaker.SetPlaceHolder(T("не определён — озвучит голос по умолчанию"))
	u.aeDubInfo.Wrapping = fyne.TextWrapWord

	save := widget.NewButtonWithIcon(T("Сохранить"), theme.DocumentSaveIcon(), func() { u.saveAudioEdit(true) })
	save.Importance = widget.HighImportance
	rec := widget.NewButtonWithIcon(T("Распознать"), theme.SearchIcon(), u.recognizeOne)
	tr := widget.NewButtonWithIcon(T("Перевести"), theme.DocumentIcon(), u.translateSpeechOne)
	closeBtn := widget.NewButtonWithIcon(T("Закрыть"), theme.CancelIcon(), u.closeAudioEditor)
	closeBtn.Importance = widget.LowImportance

	playOrig := widget.NewButtonWithIcon(T("Оригинал"), theme.MediaPlayIcon(), func() { u.togglePlay(u.aeIdx, false) })
	u.aePlayDub = widget.NewButtonWithIcon(T("Озвучка"), theme.MediaPlayIcon(), func() { u.togglePlay(u.aeIdx, true) })
	voice := widget.NewButtonWithIcon(T("Озвучить голосом"), theme.VolumeUpIcon(), u.dubOne)
	record := widget.NewButtonWithIcon(T("Записать"), theme.MediaRecordIcon(), u.recordDub)
	file := widget.NewButtonWithIcon(T("Свой файл…"), theme.FolderOpenIcon(), u.importDub)
	u.aeClear = widget.NewButtonWithIcon(T("Убрать"), theme.DeleteIcon(), u.clearDub)
	for _, b := range []*widget.Button{playOrig, u.aePlayDub, u.aeClear} {
		b.Importance = widget.LowImportance
	}
	u.aeBtns = []*widget.Button{save, rec, tr, voice, record, file, u.aeClear}

	bold := func(s string) fyne.CanvasObject {
		return widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	u.aeBox = container.NewVBox(
		widget.NewSeparator(),
		container.NewBorder(nil, nil, nil, container.NewHBox(playOrig, u.aePlayDub, closeBtn), u.aeTitle),
		container.NewGridWithColumns(2,
			container.NewBorder(bold(T("Что говорят")), nil, nil, nil, u.aeText),
			container.NewBorder(bold(T("Перевод")), nil, nil, nil, u.aeTransl)),
		container.NewHBox(save, rec, tr),
		container.NewBorder(nil, nil, bold(T("Голос персонажа:")), nil, u.aeSpeaker),
		container.NewBorder(nil, nil, bold(T("Озвучка:")), nil, u.aeDubInfo),
		container.NewHBox(voice, record, file, u.aeClear),
	)
	u.aeBox.Hide()
	return u.aeBox
}

// openAudioEditor показывает правку файла i под списком.
func (u *ui) openAudioEditor(i int) {
	if u.aeIdx >= 0 && u.aeIdx != i {
		u.saveAudioEdit(false)
	}
	u.aeIdx, u.aeFile = i, u.session.Audio[i].File
	u.loadAudioEditor()
	setVisible(u.aeBox, true)
	u.audioPane.Refresh()
	s, file := u.session, u.session.Audio[i].File
	u.goSafe(func() {
		sec, err := s.AudioSeconds(context.Background(), i)
		fyne.Do(func() {
			if err == nil && u.aeIdx == i && u.session == s {
				u.aeTitle.SetText(fmt.Sprintf(T("%s · %.1f с"), audioName(file), sec))
			}
		})
	})
}

// loadAudioEditor заполняет поля из сессии (после распознавания, перевода, озвучки).
func (u *ui) loadAudioEditor() {
	if u.aeIdx < 0 || u.session == nil || u.aeIdx >= len(u.session.Audio) {
		return
	}
	l := u.session.Audio[u.aeIdx]
	u.aeTitle.SetText(audioName(l.File))
	u.aeText.SetText(l.Transcript)
	u.aeTransl.SetText(l.Translation)
	u.aeSpeaker.SetText(l.Speaker)
	info := T("нет — в игре останется оригинал")
	switch {
	case l.Dub != "" && l.HandDub:
		info = T("своя запись")
	case l.Dub != "":
		info = T("озвучено голосом")
	}
	if l.HandText || l.HandTranslation {
		var hand []string
		if l.HandText {
			hand = append(hand, T("текст речи"))
		}
		if l.HandTranslation {
			hand = append(hand, T("перевод"))
		}
		info += T(" · вписано вручную: ") + strings.Join(hand, T(" и ")) + T(" — массовые шаги их не перезапишут")
	}
	u.aeDubInfo.SetText(info)
	if l.Dub != "" && u.fitDubs != nil && u.fitDubs.Checked {
		// как озвучка ляжет в игре — с подгонкой по длине, как при сборке
		s, i, dub := u.session, u.aeIdx, l.Dub
		u.goSafe(func() {
			_, fit, err := s.FitDub(context.Background(), i)
			fyne.Do(func() {
				if err == nil && u.session == s && u.aeIdx == i && s.Audio[i].Dub == dub {
					u.aeDubInfo.SetText(info + " · " + fit.Describe())
				}
			})
		})
	}
	setVisible(u.aePlayDub, l.Dub != "")
	setVisible(u.aeClear, l.Dub != "")
}

// syncAudioEditor — после смены списка (другая игра, другой язык) правка остаётся на том же файле или закрывается.
func (u *ui) syncAudioEditor() {
	if u.aeIdx < 0 {
		return
	}
	if u.session != nil {
		for i, l := range u.session.Audio {
			if l.File == u.aeFile {
				u.aeIdx = i
				return
			}
		}
	}
	u.aeIdx = -1
	setVisible(u.aeBox, false)
	u.audioPane.Refresh()
}

func (u *ui) closeAudioEditor() {
	u.saveAudioEdit(false)
	u.aeIdx = -1
	setVisible(u.aeBox, false)
	u.audioListW.UnselectAll()
	u.audioPane.Refresh()
}

// saveAudioEdit сохраняет поля правки, если в них что-то поменялось.
func (u *ui) saveAudioEdit(loud bool) {
	if u.aeIdx < 0 || u.session == nil || u.aeIdx >= len(u.session.Audio) {
		return
	}
	i := u.aeIdx
	l := u.session.Audio[i]
	text, tr := strings.TrimSpace(u.aeText.Text), strings.TrimSpace(u.aeTransl.Text)
	if sp := strings.TrimSpace(u.aeSpeaker.Text); sp != l.Speaker {
		if err := u.session.SetSpeaker(i, sp); err != nil {
			u.showError(err)
			return
		}
		u.refreshRow(i)
		u.updateSpeakers()
		l = u.session.Audio[i]
	}
	if text == l.Transcript && tr == l.Translation && l.Recognized {
		return
	}
	if text == "" && tr == "" && !l.Recognized && !loud {
		return // открыли и закрыли, ничего не вписав
	}
	if err := u.session.EditSpeech(i, text, tr); err != nil {
		u.showError(err)
		return
	}
	if loud {
		u.logf("%s: сохранено", audioName(l.File))
	}
	u.loadAudioEditor()
	u.refreshRow(i)
	u.updateAudioCounter()
}

func (u *ui) recognizeOne() {
	i := u.aeIdx
	stt, err := u.recognizer()
	if err != nil {
		u.showError(err)
		return
	}
	s := u.session
	s.Audio[i].HandText = false // просят распознать — значит, вписанное руками заменить
	u.run(T("Распознавание речи"), true, func(ctx context.Context) (func(), error) {
		err := s.Recognize(ctx, stt, []int{i}, u.progressFn)
		return u.afterAudioEdit, err
	})
}

func (u *ui) translateSpeechOne() {
	u.saveAudioEdit(false)
	i := u.aeIdx
	s := u.session
	if s.Audio[i].Transcript == "" {
		dialog.ShowInformation(T("Нечего переводить"), T("Сначала впиши, что говорят, или нажми «Распознать»."), u.win)
		return
	}
	tr, _, err := u.textTranslator()
	if err != nil {
		u.showError(err)
		return
	}
	l := &s.Audio[i]
	l.Translation, l.HandTranslation = "", false
	if !l.HandDub {
		l.Dub = ""
	}
	u.run(T("Перевод речи"), true, func(ctx context.Context) (func(), error) {
		err := s.TranslateAudio(ctx, tr, []int{i}, u.progressFn)
		return u.afterAudioEdit, err
	})
}

func (u *ui) dubOne() {
	u.saveAudioEdit(false)
	i := u.aeIdx
	s := u.session
	if s.Audio[i].Translation == "" {
		dialog.ShowInformation(T("Нечего озвучивать"), T("Сначала впиши перевод или нажми «Перевести»."), u.win)
		return
	}
	tts, err := u.synthesizer()
	if err != nil {
		u.showError(err)
		return
	}
	if tts == nil {
		dialog.ShowInformation(T("Озвучка выключена"), T("Выбери, чем озвучивать, в разделе «Озвучка перевода» слева."), u.win)
		return
	}
	s.Audio[i].HandDub = false
	u.startDub(tts, []int{i}, u.afterAudioEdit)
}

func (u *ui) afterAudioEdit() {
	u.loadAudioEditor()
	u.fillAudio()
}

func (u *ui) importDub() {
	i := u.aeIdx
	s := u.session
	u.pick(pickRequest{kind: pickFile, title: T("Файл озвучки"),
		patterns: []string{"*.wav", "*.mp3", "*.ogg", "*.opus", "*.flac", "*.m4a", "*.aac", "*.aif", "*.aiff", "*.bwav", "*.bfstm", "*.bfwav"},
		done: func(p string) {
			u.run(T("Своя озвучка"), false, func(ctx context.Context) (func(), error) {
				err := s.ImportDub(ctx, i, p)
				return u.afterAudioEdit, err
			})
		}})
}

func (u *ui) clearDub() {
	i := u.aeIdx
	if err := u.session.ClearDub(i); err != nil {
		u.showError(err)
	}
	u.stopPlaying()
	u.afterAudioEdit()
}

// recordDub — окно записи: текст перед глазами, длина оригинала, запись, прослушать, сохранить.
func (u *ui) recordDub() {
	u.saveAudioEdit(false)
	i := u.aeIdx
	s := u.session
	l := s.Audio[i]
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("swtr-rec-%d.wav", time.Now().UnixNano()))
	var rec *speech.Recording
	var ticker *time.Ticker

	clock := widget.NewLabelWithStyle("0:00", fyne.TextAlignCenter, fyne.TextStyle{Bold: true, Monospace: true})
	status := muted(T("Нажми «Записать» и читай. Остановить — когда закончишь."))
	var recBtn, playBtn, saveBtn *widget.Button
	stopRec := func() {
		if ticker != nil {
			ticker.Stop()
			ticker = nil
		}
		if rec == nil {
			return
		}
		err := rec.Stop()
		rec = nil
		recBtn.SetText(T("Записать заново"))
		recBtn.SetIcon(theme.MediaRecordIcon())
		if err != nil {
			setMuted(status, "")
			u.showError(err)
			return
		}
		setMuted(status, T("Готово. Послушай и сохрани — или запиши заново."))
		playBtn.Enable()
		saveBtn.Enable()
	}
	recBtn = widget.NewButtonWithIcon(T("Записать"), theme.MediaRecordIcon(), func() {
		if rec != nil {
			stopRec()
			return
		}
		speech.StopPlayback()
		r, err := speech.StartRecording(tmp)
		if err != nil {
			u.showError(err)
			return
		}
		rec = r
		recBtn.SetText(T("Остановить"))
		recBtn.SetIcon(theme.MediaStopIcon())
		playBtn.Disable()
		saveBtn.Disable()
		setMuted(status, T("Идёт запись…"))
		ticker = time.NewTicker(200 * time.Millisecond)
		go func(t *time.Ticker, r *speech.Recording) {
			for range t.C {
				fyne.Do(func() {
					if rec != r {
						return
					}
					clock.SetText(mmss(r.Elapsed().Seconds()))
					if !r.Running() {
						stopRec() // ffmpeg вышел сам — показать ошибку
					}
				})
			}
		}(ticker, r)
	})
	recBtn.Importance = widget.DangerImportance
	playBtn = widget.NewButtonWithIcon(T("Прослушать"), theme.MediaPlayIcon(), func() { u.play(tmp) })
	playBtn.Disable()
	orig := widget.NewButtonWithIcon(T("Оригинал"), theme.MediaPlayIcon(), func() { u.togglePlay(i, false) })
	saveBtn = widget.NewButtonWithIcon(T("Сохранить"), theme.DocumentSaveIcon(), nil)
	saveBtn.Disable()

	read := l.Translation
	if read == "" {
		read = T("(перевода нет — впиши его в поле «Перевод», чтобы видеть, что читать)")
	}
	text := widget.NewLabel(read)
	text.Wrapping = fyne.TextWrapWord
	text.TextStyle = fyne.TextStyle{Bold: true}
	content := container.NewVBox(
		muted(T("Оригинал: «")+oneLine(l.Transcript)+T("». Старайся уложиться в его длину — в игре реплика может обрываться по времени.")),
		text,
		clock,
		container.NewHBox(recBtn, playBtn, orig),
		status,
	)
	var d dialog.Dialog
	saveBtn.OnTapped = func() {
		stopRec()
		d.Hide()
		u.run(T("Своя озвучка"), false, func(ctx context.Context) (func(), error) {
			defer os.Remove(tmp)
			err := s.ImportDub(ctx, i, tmp)
			return u.afterAudioEdit, err
		})
	}
	cancel := widget.NewButton(T("Отмена"), func() {
		if rec != nil {
			if ticker != nil {
				ticker.Stop()
			}
			rec.Cancel()
			rec = nil
		}
		speech.StopPlayback()
		os.Remove(tmp)
		d.Hide()
	})
	d = dialog.NewCustomWithoutButtons(T("Запись озвучки — ")+audioName(l.File),
		container.NewBorder(nil, container.NewHBox(cancel, saveBtn), nil, nil, content), u.win)
	d.Resize(fyne.NewSize(560, 0))
	d.Show()
}
