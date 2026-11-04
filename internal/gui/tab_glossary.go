package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/translate"
)

// Словарь игры: собрать термины из всего текста, перевести их, поправить руками.
// Потом переводчик переводит фразы целиком, сверяясь со словарём.

type glossaryUI struct {
	glossaryInfo   *widget.Label
	glossaryNote   *widget.RichText
	glossaryBtns   []*widget.Button
	glossaryWin    fyne.Window
	glossaryList   *widget.List
	glossaryView   []int
	glossaryCount  *widget.Label
	glossarySave   *time.Timer
	glossaryFilter func() // пересобрать glossaryView по поиску
}

func (u *ui) buildGlossaryBox() fyne.CanvasObject {
	u.glossaryInfo = widget.NewLabel("")
	u.glossaryInfo.Wrapping = fyne.TextWrapWord
	u.glossaryNote = muted("")
	collect := widget.NewButtonWithIcon(T("1. Собрать словарь"), theme.SearchIcon(), u.collectGlossary)
	tr := widget.NewButtonWithIcon(T("2. Перевести словарь"), theme.DocumentIcon(), u.translateGlossary)
	open := widget.NewButtonWithIcon(T("Открыть словарь…"), theme.ListIcon(), u.openGlossary)
	u.glossaryBtns = []*widget.Button{collect, tr, open}
	return container.NewVBox(
		section(T("Словарь игры")),
		muted(T("Весь текст — реплики и распознанная речь — режется на слова, и из уникальных слов выбираются термины: ")+
			T("имена, места, предметы, приёмы, пункты меню. Они переводятся один раз, ты проверяешь и правишь, ")+
			T("а потом фразы переводятся целиком, сверяясь со словарём: термины везде одинаковые, а предложения — живым языком, с падежами.")),
		u.glossaryInfo,
		container.NewHBox(collect, tr, open),
		u.glossaryNote,
	)
}

// updateGlossaryInfo — сколько слов и терминов, переведён ли словарь, поймёт ли его переводчик.
func (u *ui) updateGlossaryInfo() {
	if u.glossaryInfo == nil {
		return
	}
	if u.session == nil {
		u.glossaryInfo.SetText("")
		return
	}
	s := u.session
	done, off := 0, 0
	for _, t := range s.Glossary {
		if t.Translation != "" {
			done++
		}
		if t.Off {
			off++
		}
	}
	text := fmt.Sprintf(T("Слов в тексте и речи: %s, разных: %s."), groupDigits(s.Words.Words), groupDigits(s.Words.Unique))
	switch {
	case len(s.Glossary) == 0:
		text += T(" Словарь ещё не собран.")
	default:
		text += fmt.Sprintf(T(" В словаре %d терминов, переведено %d."), len(s.Glossary), done)
		if off > 0 {
			text += fmt.Sprintf(T(" Выключено %d."), off)
		}
	}
	u.glossaryInfo.SetText(text)
	note := ""
	if o, ok := u.currentTextOption(); ok && !o.LLM && len(s.Glossary) > 0 {
		note = "«" + o.Title + T("» словарь не понимает — термины он переведёт как получится. Словарь учитывают ИИ-переводчики: ChatGPT, Claude, Codex, Claude Code.")
	}
	if pending := u.unrecognizedSpeech(); pending > 0 {
		if note != "" {
			note += " "
		}
		note += fmt.Sprintf(T("На вкладке «Речь в текст» отмечено %d файлов, где речь ещё не распознана: распознай её сначала — тогда она попадёт и в словарь, и в перевод."), pending)
	}
	setMuted(u.glossaryNote, note)
	setVisible(u.glossaryNote, note != "")
	if u.textLeft != nil {
		u.textLeft.Refresh() // подписи стали длиннее или короче — переложить колонку
	}
}

// unrecognizedSpeech — сколько отмеченных звуковых файлов ещё не распознано.
func (u *ui) unrecognizedSpeech() int {
	n := 0
	for _, l := range u.session.Audio {
		if l.Selected && !l.Recognized {
			n++
		}
	}
	return n
}

func groupDigits(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

func (u *ui) collectGlossary() {
	s := u.session
	if s == nil {
		return
	}
	to := u.toLang()
	if err := s.BuildGlossary(to); err != nil {
		u.showError(err)
	}
	u.updateGlossaryInfo()
	u.refreshGlossary()
	if len(s.Glossary) > 0 {
		u.openGlossary()
	}
}

func (u *ui) translateGlossary() {
	s := u.session
	if s == nil {
		return
	}
	if len(s.Glossary) == 0 {
		u.collectGlossary()
		if len(s.Glossary) == 0 {
			dialog.ShowInformation(T("Словарь пуст"), T("В тексте не нашлось имён и терминов. Можно добавить их вручную в «Открыть словарь»."), u.win)
			return
		}
	}
	tr, _, err := u.textTranslator()
	if err != nil {
		u.showError(err)
		return
	}
	to := u.toLang()
	u.run(T("Перевод словаря"), true, func(ctx context.Context) (func(), error) {
		err := s.TranslateGlossary(ctx, tr, to, u.progressFn)
		return func() {
			u.updateGlossaryInfo()
			u.refreshGlossary()
		}, err
	})
}

// openGlossary — окно словаря: включить/выключить статью, поправить перевод, добавить свою.
func (u *ui) openGlossary() {
	if u.session == nil {
		return
	}
	if u.glossaryWin != nil {
		u.refreshGlossary()
		u.glossaryWin.RequestFocus()
		return
	}
	w := u.app.NewWindow(T("Словарь игры"))
	u.glossaryWin = w
	w.SetOnClosed(func() {
		u.flushGlossary()
		u.glossaryWin, u.glossaryList, u.glossaryFilter = nil, nil, nil
		u.updateGlossaryInfo()
	})
	search := widget.NewEntry()
	search.SetPlaceHolder(T("Поиск по словарю"))
	u.glossaryCount = widget.NewLabel("")
	src := widget.NewEntry()
	src.SetPlaceHolder(T("слово или имя из игры"))
	dst := widget.NewEntry()
	dst.SetPlaceHolder(T("как переводить"))
	add := widget.NewButtonWithIcon(T("Добавить"), theme.ContentAddIcon(), func() {
		if err := u.session.AddTerm(u.toLang(), src.Text, dst.Text); err != nil {
			u.showError(err)
			return
		}
		src.SetText("")
		dst.SetText("")
		search.SetText("")
		u.refreshGlossary()
	})
	u.glossaryFilter = func() {
		u.glossaryView = u.glossaryView[:0]
		q := strings.ToLower(strings.TrimSpace(search.Text))
		for i, t := range u.session.Glossary {
			if q == "" || strings.Contains(strings.ToLower(t.Source+" "+t.Translation), q) {
				u.glossaryView = append(u.glossaryView, i)
			}
		}
	}
	search.OnChanged = func(string) { u.refreshGlossary() }
	on := widget.NewButtonWithIcon(T("Включить найденные"), theme.CheckButtonCheckedIcon(), func() { u.setTermsOff(false) })
	off := widget.NewButtonWithIcon(T("Выключить найденные"), theme.CheckButtonIcon(), func() { u.setTermsOff(true) })
	for _, b := range []*widget.Button{on, off} {
		b.Importance = widget.LowImportance
	}

	u.glossaryList = widget.NewList(
		func() int { return len(u.glossaryView) },
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			name := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			name.Truncation = fyne.TextTruncateEllipsis
			ex := widget.NewRichText(&widget.TextSegment{Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder}})
			ex.Truncation = fyne.TextTruncateEllipsis
			entry := widget.NewEntry()
			entry.SetPlaceHolder(T("перевод"))
			left := container.New(tightVBox(), name, ex)
			return container.NewBorder(nil, nil, chk, nil, container.NewGridWithColumns(2, left, entry))
		},
		func(row widget.ListItemID, o fyne.CanvasObject) {
			if row >= len(u.glossaryView) {
				return
			}
			i := u.glossaryView[row]
			t := u.session.Glossary[i]
			c := o.(*fyne.Container)
			grid, chk := c.Objects[0].(*fyne.Container), c.Objects[1].(*widget.Check)
			left, entry := grid.Objects[0].(*fyne.Container), grid.Objects[1].(*widget.Entry)
			chk.OnChanged = nil
			chk.SetChecked(!t.Off)
			chk.OnChanged = func(v bool) {
				u.session.Glossary[i].Off = !v
				u.saveGlossarySoon()
			}
			mark := ""
			if t.Hand {
				mark = " ✎"
			}
			left.Objects[0].(*widget.Label).SetText(fmt.Sprintf("%s · %d×%s", t.Source, t.Count, mark))
			ex := left.Objects[1].(*widget.RichText)
			ex.Segments[0].(*widget.TextSegment).Text = t.Example
			ex.Refresh()
			entry.OnChanged = nil
			entry.SetText(t.Translation)
			entry.OnChanged = func(v string) {
				g := &u.session.Glossary[i]
				g.Translation, g.Hand = strings.TrimSpace(v), true
				u.saveGlossarySoon()
			}
		})
	head := container.NewVBox(
		muted(T("Галочка — подсказывать переводчику. Выключи то, что словом-термином не является (обычные слова с большой буквы). ")+
			T("Перевод правится прямо в строке и сохраняется сам. Имена и названия, которые не надо переводить, впиши как есть.")),
		container.NewBorder(nil, nil, nil, container.NewHBox(on, off), search),
		u.glossaryCount,
	)
	addRow := container.NewBorder(nil, nil, nil, add, container.NewGridWithColumns(2, src, dst))
	w.SetContent(container.NewBorder(head, container.NewVBox(widget.NewSeparator(), addRow), nil, nil, u.glossaryList))
	w.Resize(fyne.NewSize(900, 640))
	u.refreshGlossary()
	w.Show()
}

func (u *ui) refreshGlossary() {
	if u.glossaryList == nil || u.session == nil {
		return
	}
	u.glossaryFilter()
	done := 0
	for _, t := range u.session.Glossary {
		if t.Translation != "" {
			done++
		}
	}
	u.glossaryCount.SetText(fmt.Sprintf(T("Показано %d из %d · переведено %d"), len(u.glossaryView), len(u.session.Glossary), done))
	u.glossaryList.Refresh()
}

func (u *ui) setTermsOff(off bool) {
	for _, i := range u.glossaryView {
		u.session.Glossary[i].Off = off
	}
	u.saveGlossarySoon()
	u.glossaryList.Refresh()
}

// saveGlossarySoon сохраняет словарь через полсекунды после последней правки — не на каждую букву.
func (u *ui) saveGlossarySoon() {
	if u.glossarySave != nil {
		u.glossarySave.Stop()
	}
	u.glossarySave = time.AfterFunc(500*time.Millisecond, func() { fyne.Do(u.flushGlossary) })
}

func (u *ui) flushGlossary() {
	if u.glossarySave != nil {
		u.glossarySave.Stop()
		u.glossarySave = nil
	}
	if u.session == nil {
		return
	}
	if err := u.session.SaveGlossary(u.toLang()); err != nil {
		u.logf("Не удалось сохранить словарь: %v", err)
	}
}

func (u *ui) currentTextOption() (translate.Option, bool) {
	if u.textProv == nil || u.textProv.Selected == "" {
		return translate.Option{}, false
	}
	opts := translate.Options()
	return opts[indexOf(labelsOf(opts), u.textProv.Selected)], true
}
