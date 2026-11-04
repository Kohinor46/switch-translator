package gui

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"switch-translator/internal/i18n"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/ffmpeg"
	"switch-translator/internal/nx"
	"switch-translator/internal/project"
)

// videoTab — ролики игры: их речь распознаётся на вкладке «Речь в текст», переводится вместе с текстом,
// а субтитры и озвучка вшиваются на вкладке «Озвучка». Список роликов переезжает между вкладками, как и звук.
type videoTab struct {
	videoPane    fyne.CanvasObject
	videoListW   *widget.List
	videoCounter *widget.Label
	videoSubs    *widget.Check
	videoDub     *widget.Check
	videoActs    []*widget.Button
}

func (u *ui) videoButtons() []*widget.Button { return u.videoActs }

// buildVideoTab строит список роликов (u.videoPane) и их шаги для двух этапов.
func (u *ui) buildVideoTab() (recognizeSide, dubSide []fyne.CanvasObject) {
	u.videoSubs = widget.NewCheck(T("Вшить субтитры с переводом"), func(on bool) { u.prefs.SetBool("videoSubs", on) })
	u.videoSubs.Checked = u.prefs.BoolWithFallback("videoSubs", true)
	u.videoDub = widget.NewCheck(T("Подмешать озвучку перевода"), func(on bool) { u.prefs.SetBool("videoDub", on) })
	u.videoDub.Checked = u.prefs.BoolWithFallback("videoDub", false)

	recognize := widget.NewButtonWithIcon(T("Распознать речь в роликах"), theme.SearchIcon(), u.recognizeVideo)
	dub := widget.NewButtonWithIcon(T("Озвучить фразы роликов"), theme.VolumeUpIcon(), u.dubVideo)
	render := widget.NewButtonWithIcon(T("Собрать ролики"), theme.MediaVideoIcon(), u.renderVideo)

	ff := widget.NewRichText()
	ff.Wrapping = fyne.TextWrapWord
	if ffmpeg.Available() {
		richStatus(ff, true, T("ffmpeg встроен"), T("Ролики пересобираются тем же кодеком, в том же размере кадра и с той же частотой — так игра их проиграет."))
	} else {
		richStatus(ff, false, T("В эту сборку программы ffmpeg не встроен"),
			T("Перед сборкой программы запусти tools/fetch-ffmpeg.sh — без ffmpeg работать с роликами нельзя."))
	}

	recognizeSide = []fyne.CanvasObject{
		widget.NewSeparator(),
		section(T("Ролики")),
		muted(T("Отметь ролики, где есть речь (справа, «Ролики»).")),
		step(recognize, T("Находит речь и делит её на фразы со временем. В роликах только с музыкой речи не будет.")),
	}
	dubSide = []fyne.CanvasObject{
		widget.NewSeparator(),
		section(T("Ролики")),
		ff,
		u.videoSubs, u.videoDub,
		muted(T("Субтитры — белый текст с обводкой внизу кадра. Озвучка звучит поверх оригинала, а сам оригинал на время фразы приглушается; ") +
			T("слишком длинная фраза немного ускоряется, чтобы успеть до следующей. Перевод фраз можно поправить кнопкой «Фразы» у ролика.")),
		step(dub, T("Нужно, только если ролик озвучивается: каждая фраза — голосом, выбранным выше.")),
		step(render, T("Пересобирает отмеченные ролики с субтитрами и (или) озвучкой. Готовый ролик можно посмотреть справа.")),
	}

	u.videoCounter = widget.NewLabel("")
	selAll := widget.NewButton(T("Отметить все"), func() { u.markVideo(func(*project.VideoLine) bool { return true }) })
	selNone := widget.NewButton(T("Снять все"), func() { u.markVideo(func(*project.VideoLine) bool { return false }) })
	selSpeech := widget.NewButton(T("Только с речью"), func() { u.markVideo(func(l *project.VideoLine) bool { return l.HasSpeech() }) })
	for _, b := range []*widget.Button{selAll, selNone, selSpeech} {
		b.Importance = widget.LowImportance
	}
	u.videoActs = []*widget.Button{recognize, dub, render, selAll, selNone, selSpeech}

	u.videoListW = widget.NewList(
		func() int {
			if u.session == nil {
				return 0
			}
			return len(u.session.Video)
		},
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			play := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), nil)
			play.Importance = widget.LowImportance
			edit := widget.NewButton(T("Фразы"), nil)
			edit.Importance = widget.LowImportance
			result := widget.NewButtonWithIcon(T("Перевод"), theme.MediaPlayIcon(), nil)
			result.Importance = widget.LowImportance
			name := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			name.Truncation = fyne.TextTruncateEllipsis
			info := widget.NewRichText(&widget.TextSegment{Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder}})
			info.Truncation = fyne.TextTruncateEllipsis
			text := container.New(tightVBox(), name, info)
			return container.NewBorder(nil, nil, container.NewHBox(chk, play), container.NewHBox(edit, result), text)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			l := u.session.Video[i]
			c := o.(*fyne.Container)
			text := c.Objects[0].(*fyne.Container)
			left := c.Objects[1].(*fyne.Container)
			right := c.Objects[2].(*fyne.Container)
			chk, play := left.Objects[0].(*widget.Check), left.Objects[1].(*widget.Button)
			edit, result := right.Objects[0].(*widget.Button), right.Objects[1].(*widget.Button)
			chk.OnChanged = nil
			chk.SetChecked(l.Selected)
			chk.OnChanged = func(on bool) {
				u.session.Video[i].Selected = on
				u.session.SaveVideo()
				u.updateVideoCounter()
			}
			play.OnTapped = func() { u.openVideo(i) }
			text.Objects[0].(*widget.Label).SetText(fmt.Sprintf("%s · %s", path.Base(l.File), nx.HumanSize(l.Size)))
			info := text.Objects[1].(*widget.RichText)
			info.Segments[0].(*widget.TextSegment).Text = videoInfo(&l)
			info.Refresh()
			setVisible(edit, l.HasSpeech())
			edit.OnTapped = func() { u.editPhrases(i) }
			if l.Output != "" {
				result.Show()
				out := l.Output
				result.OnTapped = func() { u.app.OpenURL(&url.URL{Scheme: "file", Path: out}) }
			} else {
				result.Hide()
			}
		})

	head := container.NewVBox(
		muted(T("▶ — посмотреть ролик в плеере компьютера. Галочка — работать с этим роликом.")),
		u.videoCounter,
		container.NewHBox(selAll, selNone, selSpeech),
	)
	u.videoPane = container.NewBorder(head, nil, nil, nil, u.videoListW)
	return recognizeSide, dubSide
}

func videoInfo(l *project.VideoLine) string {
	var parts []string
	if l.Info != nil {
		parts = append(parts, l.Info.Describe())
	} else {
		parts = append(parts, i18n.Phrases(l.Format))
	}
	switch {
	case l.Unsupported != "":
		parts = append(parts, l.Unsupported)
	case l.Error != "":
		parts = append(parts, T("Ошибка: ")+l.Error)
	case !l.Recognized:
		parts = append(parts, T("речь ещё не распознана"))
	case !l.HasSpeech():
		parts = append(parts, T("речи нет"))
	default:
		s := fmt.Sprintf(T("фраз: %d, переведено: %d"), len(l.Segments), l.Translated())
		if l.Output != "" {
			s += T(", ролик собран")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func (u *ui) fillVideo() {
	if u.videoListW == nil {
		return
	}
	u.videoListW.Refresh()
	u.updateVideoCounter()
	if u.session != nil {
		u.updateMediaTabs()
	}
}

func (u *ui) updateVideoCounter() {
	if u.session == nil {
		u.videoCounter.SetText("")
		return
	}
	sel := len(u.selectedVideo())
	if len(u.session.Video) == 0 {
		u.videoCounter.SetText(T("Роликов в игре нет."))
		return
	}
	u.videoCounter.SetText(fmt.Sprintf(T("Роликов: %d, отмечено: %d"), len(u.session.Video), sel))
}

func (u *ui) markVideo(keep func(*project.VideoLine) bool) {
	if u.session == nil {
		return
	}
	for i := range u.session.Video {
		l := &u.session.Video[i]
		l.Selected = l.Unsupported == "" && keep(l)
	}
	u.session.SaveVideo()
	u.fillVideo()
}

func (u *ui) selectedVideo() []int {
	var idx []int
	if u.session == nil {
		return nil
	}
	for i, l := range u.session.Video {
		if l.Selected {
			idx = append(idx, i)
		}
	}
	return idx
}

func (u *ui) needVideo() []int {
	if u.session == nil {
		return nil
	}
	if !ffmpeg.Available() {
		u.showError(ffmpeg.ErrMissing)
		return nil
	}
	idx := u.selectedVideo()
	if len(idx) == 0 {
		dialog.ShowInformation(T("Ничего не отмечено"), T("Отметь галочками ролики справа — шаг работает только с ними."), u.win)
	}
	return idx
}

// openVideo открывает оригинал ролика в плеере компьютера (сначала он извлекается из образа).
func (u *ui) openVideo(i int) {
	file := u.session.Video[i].File
	s := u.session
	u.goSafe(func() {
		src, err := s.VideoSource(file)
		fyne.Do(func() {
			if err != nil {
				u.showError(err)
				return
			}
			u.app.OpenURL(&url.URL{Scheme: "file", Path: src})
		})
	})
}

func (u *ui) recognizeVideo() {
	idx := u.needVideo()
	if len(idx) == 0 {
		return
	}
	stt, err := u.recognizer()
	if err != nil {
		u.showError(err)
		return
	}
	s := u.session
	u.run(T("Распознавание речи в роликах"), true, func(ctx context.Context) (func(), error) {
		err := s.RecognizeVideo(ctx, stt, idx, u.progressFn)
		return u.fillVideo, err
	})
}

func (u *ui) dubVideo() {
	idx := u.needVideo()
	if len(idx) == 0 {
		return
	}
	tts, err := u.synthesizer()
	if err != nil {
		u.showError(err)
		return
	}
	if tts == nil {
		dialog.ShowInformation(T("Озвучка выключена"), T("Выбери, чем озвучивать, на вкладке «Озвучка» в разделе «Голос»."), u.win)
		return
	}
	s := u.session
	u.run(T("Озвучка роликов"), true, func(ctx context.Context) (func(), error) {
		err := s.DubVideo(ctx, tts, idx, u.progressFn)
		return u.fillVideo, err
	})
}

func (u *ui) renderVideo() {
	idx := u.needVideo()
	if len(idx) == 0 {
		return
	}
	if !u.videoSubs.Checked && !u.videoDub.Checked {
		dialog.ShowInformation(T("Нечего делать"), T("Отметь «Вшить субтитры» или «Подмешать озвучку»."), u.win)
		return
	}
	s := u.session
	for _, i := range idx {
		s.Video[i].Subtitles, s.Video[i].Dubbing = u.videoSubs.Checked, u.videoDub.Checked
	}
	u.run(T("Сборка роликов"), true, func(ctx context.Context) (func(), error) {
		err := s.RenderVideo(ctx, idx, u.progressFn)
		return func() { u.fillVideo(); u.updateBuildInfo() }, err
	})
}

// editPhrases — окно с фразами ролика: время, оригинал и поле перевода.
func (u *ui) editPhrases(i int) {
	l := &u.session.Video[i]
	entries := make([]*widget.Entry, len(l.Segments))
	rows := container.NewVBox()
	for k, sg := range l.Segments {
		e := widget.NewMultiLineEntry()
		e.Wrapping = fyne.TextWrapWord
		e.SetMinRowsVisible(2)
		e.SetText(sg.Translation)
		entries[k] = e
		orig := widget.NewLabel(fmt.Sprintf("%s–%s  %s", clock(sg.Start), clock(sg.End), sg.Text))
		orig.Wrapping = fyne.TextWrapWord
		rows.Add(container.NewVBox(orig, e))
	}
	scroll := container.NewVScroll(rows)
	scroll.SetMinSize(fyne.NewSize(640, 420))
	dialog.ShowCustomConfirm(path.Base(l.File)+T(" — фразы"), T("Сохранить"), T("Отмена"), scroll, func(ok bool) {
		if !ok {
			return
		}
		for k, e := range entries {
			if strings.TrimSpace(e.Text) != l.Segments[k].Translation {
				l.Segments[k].Translation = strings.TrimSpace(e.Text)
				l.Segments[k].Dub = "" // старая озвучка больше не подходит
			}
		}
		u.session.SaveVideo()
		u.fillVideo()
	}, u.win)
}

func clock(t float64) string {
	s := int(t)
	return fmt.Sprintf("%d:%02d.%d", s/60, s%60, int((t-float64(s))*10))
}
