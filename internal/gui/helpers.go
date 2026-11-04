package gui

import (
	"net/url"
	"regexp"
	"strings"
	"switch-translator/internal/i18n"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// helpContent — «как подключить»: шаги, команды с кнопкой «Копировать» и ссылка на сайт.
func (u *ui) helpContent(site, help string, commands []string) fyne.CanvasObject {
	var objs []fyne.CanvasObject
	if help != "" {
		rt := widget.NewRichTextFromMarkdown(i18n.Msg(help))
		rt.Wrapping = fyne.TextWrapWord
		objs = append(objs, rt)
	}
	for _, c := range commands {
		objs = append(objs, u.commandRow(c))
	}
	if site != "" {
		if link, err := url.Parse(site); err == nil {
			objs = append(objs, container.NewHBox(widget.NewLabel(T("Сайт:")), widget.NewHyperlink(link.Host+link.Path, link)))
		}
	}
	return container.NewVBox(objs...)
}

// commandRow — команда моноширинным шрифтом (её можно выделить) и кнопка «Копировать».
func (u *ui) commandRow(cmd string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(cmd, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	l.Selectable = true
	l.Wrapping = fyne.TextWrapBreak
	var btn *widget.Button
	btn = widget.NewButtonWithIcon(T("Копировать"), theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(cmd)
		btn.SetText(T("Скопировано"))
		time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(func() { btn.SetText(T("Копировать")) }) })
	})
	btn.Importance = widget.LowImportance
	return container.NewBorder(nil, nil, nil, btn, l)
}

// setHelp кладёт в box раскрывающийся блок «Как подключить…»; open — сразу раскрыть.
func setHelp(box *fyne.Container, title string, content fyne.CanvasObject, open bool) {
	if content == nil {
		box.Objects = nil
		box.Refresh()
		return
	}
	acc := widget.NewAccordion(widget.NewAccordionItem(title, content))
	if open {
		acc.Open(0)
	}
	box.Objects = []fyne.CanvasObject{acc}
	box.Refresh()
}

// richStatus — строка состояния: зелёная галочка или предупреждение, плюс пояснение.
func richStatus(rt *widget.RichText, ok bool, text, note string) {
	style := widget.RichTextStyle{ColorName: theme.ColorNameSuccess, Inline: true, TextStyle: fyne.TextStyle{Bold: true}}
	prefix := "✓ "
	if !ok {
		style.ColorName = theme.ColorNameWarning
		prefix = ""
	}
	segs := []widget.RichTextSegment{&widget.TextSegment{Text: prefix + text, Style: style}}
	if note != "" {
		segs = append(segs, &widget.TextSegment{Text: "  " + note, Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder, Inline: true}})
	}
	rt.Segments = segs
	rt.Refresh()
}

// commandRe — команды, которые стоит дать скопировать из текста ошибки.
var commandRe = regexp.MustCompile(`(?:brew|winget|apt|sudo|npm|pip|curl|claude|codex)\s[^«»"\n]+`)

// showError записывает ошибку в лог и показывает окно, из которого текст (и команду, если она есть) можно скопировать.
func (u *ui) showError(err error) {
	if err == nil {
		return
	}
	u.addLog(T("Ошибка: ") + i18n.Err(err))
	u.errorDialog(err)
}

// errorDialog — окно ошибки с выделяемым текстом и кнопками «Копировать».
func (u *ui) errorDialog(err error) {
	msg := i18n.Err(err)
	text := widget.NewEntry()
	text.MultiLine = true
	text.Wrapping = fyne.TextWrapWord
	text.SetText(msg)
	text.OnChanged = func(string) { text.SetText(msg) } // только для чтения, но выделять и копировать можно
	text.SetMinRowsVisible(min(8, max(2, len(msg)/70+1)))
	btns := container.NewHBox(widget.NewButtonWithIcon(T("Копировать текст"), theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(msg)
	}))
	if cmd := strings.TrimSpace(commandRe.FindString(msg)); cmd != "" {
		btns.Add(widget.NewButtonWithIcon(T("Копировать команду"), theme.ContentCopyIcon(), func() {
			u.app.Clipboard().SetContent(cmd)
		}))
	}
	content := container.NewVBox(text, btns)
	d := dialog.NewCustom(T("Ошибка"), "OK", content, u.win)
	d.Resize(fyne.NewSize(620, 0))
	d.Show()
}
