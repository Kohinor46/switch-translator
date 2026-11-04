package gui

import (
	"os"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logView хранит лог и показывает его последние строки. Писать в него можно из любой горутины.
type logView struct {
	mu      sync.Mutex
	lines   []string
	pending bool // обновление экрана уже запланировано
	label   *widget.Label
	scroll  *container.Scroll
	last    *widget.Label // последняя строка — видна, пока панель свёрнута
}

const (
	maxLogLines  = 500    // показываются в панели
	maxKeptLines = 20_000 // хранятся для «Копировать» и «Сохранить»
)

func newLogView() *logView {
	l := &logView{label: widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})}
	l.label.Selectable = true
	l.scroll = container.NewScroll(l.label)
	l.last = widget.NewLabel("")
	l.last.Truncation = fyne.TextTruncateEllipsis
	return l
}

func (l *logView) add(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, strings.Split(strings.TrimRight(line, "\n"), "\n")...)
	if len(l.lines) > maxKeptLines {
		l.lines = l.lines[len(l.lines)-maxKeptLines:]
	}
	// пачку строк подряд рисуем одним обновлением
	schedule := !l.pending
	l.pending = true
	l.mu.Unlock()
	if schedule {
		fyne.Do(l.flush)
	}
}

func (l *logView) flush() {
	l.mu.Lock()
	text := strings.Join(l.lines[max(0, len(l.lines)-maxLogLines):], "\n")
	last := ""
	if len(l.lines) > 0 {
		last = l.lines[len(l.lines)-1]
	}
	l.pending = false
	l.mu.Unlock()
	l.label.SetText(text)
	l.last.SetText(last)
	l.scroll.ScrollToBottom()
}

// Text — весь сохранённый лог.
func (l *logView) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logView) Clear() {
	l.mu.Lock()
	l.lines = nil
	l.mu.Unlock()
	l.label.SetText("")
	l.last.SetText("")
}

// withLog показывает main с панелью лога под ней. Панель свёрнута до последней строки,
// кнопка «Лог» разворачивает её.
func (u *ui) withLog(main fyne.CanvasObject) {
	clear := widget.NewButtonWithIcon(T("Очистить"), theme.DeleteIcon(), u.logView.Clear)
	var copyBtn *widget.Button
	copyBtn = widget.NewButtonWithIcon(T("Копировать"), theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(u.logView.Text())
		copyBtn.SetText(T("Скопировано"))
		time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(func() { copyBtn.SetText(T("Копировать")) }) })
	})
	var save *widget.Button
	save = widget.NewButtonWithIcon(T("Сохранить…"), theme.DocumentSaveIcon(), func() {
		u.pick(pickRequest{
			kind:     pickSave,
			title:    T("Сохранить лог"),
			filename: "switch-translator-log-" + time.Now().Format("2006-01-02-150405") + ".txt",
			btn:      save,
			done:     u.saveLog,
		})
	})
	for _, b := range []*widget.Button{clear, copyBtn, save} {
		b.Importance = widget.LowImportance
	}
	u.logToggle = widget.NewButtonWithIcon(T("Лог"), nil, func() { u.setLogOpen(!u.logOpen) })
	u.logToggle.Importance = widget.LowImportance
	header := container.NewBorder(nil, nil, u.logToggle, container.NewHBox(copyBtn, save, clear), u.logView.last)
	u.renderLog = func() {
		if u.logOpen {
			u.logToggle.SetIcon(theme.MenuDropDownIcon())
			u.logView.last.Hide()
			split := container.NewVSplit(main, container.NewBorder(header, nil, nil, nil, u.logView.scroll))
			split.Offset = 0.65
			u.win.SetContent(container.NewPadded(split))
			u.logView.scroll.ScrollToBottom()
		} else {
			u.logToggle.SetIcon(theme.MenuDropUpIcon())
			u.logView.last.Show()
			u.win.SetContent(container.NewPadded(container.NewBorder(nil, container.NewVBox(widget.NewSeparator(), header), nil, nil, main)))
		}
	}
	u.renderLog()
}

func (u *ui) setLogOpen(open bool) {
	u.logOpen = open
	u.renderLog()
	if u.logMenu != nil {
		u.logMenu.Label = map[bool]string{true: T("Скрыть лог"), false: T("Показать лог")}[open]
		u.win.MainMenu().Refresh()
	}
}

func (u *ui) saveLog(dest string) {
	if err := os.WriteFile(dest, []byte(u.logView.Text()+"\n"), 0o644); err != nil {
		u.showError(err)
		return
	}
	u.logf("Лог сохранён: %s", dest)
}
