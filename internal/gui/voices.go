package gui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/speech"
)

// Голоса персонажей: разделить реплики по голосам (вкладка «Речь в текст»), назвать голоса,
// выбрать или скопировать каждому свой голос озвучки (вкладка «Озвучка»).

const autoCount = "определить по звуку"

// splitVoices делит отмеченные реплики по голосам; если различение голосов не установлено — сначала ставит его.
func (u *ui) splitVoices() {
	idx := u.needSelected()
	if len(idx) == 0 {
		return
	}
	counts := []string{T(autoCount)}
	for n := 2; n <= 20; n++ {
		counts = append(counts, strconv.Itoa(n))
	}
	count := widget.NewSelect(counts, nil)
	count.SetSelected(T(autoCount))
	ready, need := speech.SpeakersReady()
	items := []*widget.FormItem{
		widget.NewFormItem(T("Сколько голосов"), count),
		widget.NewFormItem("", muted(T("Знаешь, сколько в игре говорящих персонажей, — укажи: так точнее. ")+
			T("Реплики без распознанной речи пропускаются."))),
	}
	if !ready {
		items = append(items, widget.NewFormItem("", muted(need)))
	}
	dialog.ShowForm(T("Разделить по голосам"), T("Разделить"), T("Отмена"), items, func(ok bool) {
		if !ok {
			return
		}
		want, _ := strconv.Atoi(count.Selected)
		s := u.session
		u.run(T("Разделение по голосам"), true, func(ctx context.Context) (func(), error) {
			if ready, _ := speech.SpeakersReady(); !ready {
				err := speech.SetupSpeakers(ctx, func(line string) { u.logf("%s", line) }, func(done, total int64) {
					u.progressFn(T("Различение голосов"), float64(done), float64(total))
				})
				if err != nil {
					return nil, err
				}
			}
			err := s.SplitVoices(ctx, idx, want, u.progressFn)
			return func() {
				u.fillAudio()
				u.updateSpeakers()
			}, err
		})
	}, u.win)
}

// updateSpeakers показывает раздел персонажей на «Озвучке», когда реплики уже разделены по голосам.
func (u *ui) updateSpeakers() {
	if u.voicesBox == nil {
		return
	}
	n := 0
	if u.session != nil {
		n = len(u.session.Speakers())
	}
	setVisible(u.voicesBox, n > 0)
	if n > 0 {
		u.speakersBtn.SetText(fmt.Sprintf(T("Голоса персонажей (%d)…"), n))
	}
	if u.aeSpeaker != nil && u.session != nil {
		var names []string
		for _, sp := range u.session.Speakers() {
			names = append(names, sp.Name)
		}
		u.aeSpeaker.SetOptions(names)
	}
}

// perVoice — озвучивать ли каждого персонажа своим голосом.
func (u *ui) perVoice() bool {
	return u.perVoiceChk != nil && u.perVoiceChk.Checked && u.session != nil && len(u.session.Speakers()) > 0
}

// startDub озвучивает реплики idx: голосами персонажей или одним выбранным голосом.
func (u *ui) startDub(tts speech.Synthesizer, idx []int, after func()) {
	s := u.session
	o, cfg := u.currentTTS(), u.ttsConfig()
	per := u.perVoice()
	u.run(T("Озвучка"), true, func(ctx context.Context) (func(), error) {
		if per {
			return after, s.DubVoices(ctx, o.ID, cfg, idx, u.progressFn)
		}
		return after, s.Dub(ctx, tts, idx, u.progressFn)
	})
}

// speakersDialog — голоса персонажей: имя, сколько реплик, послушать, голос озвучки, скопировать голос.
func (u *ui) speakersDialog() {
	s := u.session
	if s == nil {
		return
	}
	o := u.currentTTS()
	cfg := u.ttsConfig()
	rows := container.NewVBox()
	var d dialog.Dialog
	var fill func()
	fill = func() {
		rows.Objects = nil
		header := fmt.Sprintf(T("Голос озвучки — для «%s». Пусто — "), o.Title)
		if o.Clone {
			header += T("голос скопируется по репликам персонажа при озвучке (или нажми «Скопировать» сейчас).")
		} else {
			header += T("голос, выбранный на вкладке. Копировать голос эта озвучка не умеет — выбери каждому голос из списка.")
		}
		rows.Add(muted(header + T(" Переименуй голос в имя персонажа; имя другого голоса сольёт их в один.")))
		for _, sp := range s.Speakers() {
			name := sp.Name
			nameE := widget.NewEntry()
			nameE.SetText(name)
			rename := func() {
				if err := s.RenameSpeaker(name, nameE.Text); err != nil {
					u.showError(err)
					return
				}
				fill()
				u.fillAudio()
				u.updateSpeakers()
			}
			nameE.OnSubmitted = func(string) { rename() }
			ok := widget.NewButtonWithIcon("", theme.ConfirmIcon(), rename)
			ok.Importance = widget.LowImportance

			voice := widget.NewSelectEntry(u.ttsVoices)
			if o.Clone {
				voice.SetPlaceHolder(T("скопируется при озвучке"))
			} else {
				voice.SetPlaceHolder(T("голос, выбранный на вкладке"))
			}
			voice.SetText(s.SpeakerVoice(name, o.ID))
			voice.OnChanged = func(v string) {
				if err := s.SetSpeakerVoice(name, o.ID, strings.TrimSpace(v)); err != nil {
					u.showError(err)
				}
			}
			listen := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() {
				u.goSafe(func() {
					wavs, _, err := s.VoiceSamples(context.Background(), name, 1)
					fyne.Do(func() {
						if err == nil && len(wavs) == 0 {
							err = fmt.Errorf(T("у «%s» нет реплик длиннее секунды"), name)
						}
						if err != nil {
							u.showError(err)
							return
						}
						u.play(wavs[0])
					})
				})
			})
			listen.Importance = widget.LowImportance
			mark := widget.NewButtonWithIcon(T("Отметить"), theme.CheckButtonCheckedIcon(), func() {
				for i := range s.Audio {
					s.Audio[i].Selected = s.Audio[i].Speaker == name
				}
				s.SaveAudio()
				u.fillAudio()
				u.logf("Отмечены только реплики «%s»", name)
			})
			mark.Importance = widget.LowImportance
			acts := container.NewHBox(listen, mark)
			if o.Clone {
				clone := widget.NewButtonWithIcon(T("Скопировать"), theme.ContentCopyIcon(), func() {
					if o.NeedsKey && cfg.APIKey == "" {
						dialog.ShowInformation(T("Нужен ключ ")+o.Title, T("Вставь API-ключ ")+o.Title+T(" в поле «Ключ»."), u.win)
						return
					}
					u.run(T("Копирование голоса"), true, func(ctx context.Context) (func(), error) {
						label, err := s.CloneSpeaker(ctx, o.ID, cfg, name)
						if err != nil {
							return nil, err
						}
						return func() {
							voice.SetText(label)
							u.reloadVoices()
						}, nil
					})
				})
				clone.Importance = widget.LowImportance
				acts.Add(clone)
			}
			left := container.NewBorder(nil, nil, nil, ok, nameE)
			info := widget.NewLabel(fmt.Sprintf(T("%d реплик"), sp.Lines))
			rows.Add(container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(260, nameE.MinSize().Height), left),
				container.NewHBox(info, acts), voice))
		}
		rows.Refresh()
	}
	fill()
	d = dialog.NewCustom(T("Голоса персонажей"), T("Закрыть"), container.NewVScroll(rows), u.win)
	d.Resize(fyne.NewSize(1000, 560))
	d.Show()
}
