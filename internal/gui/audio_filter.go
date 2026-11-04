package gui

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/project"
)

// Инструменты для больших списков звука: группы (архив, сцена, папка), вид звука,
// действия над всем найденным и Shift+клик по галочкам — отметить диапазон.

// audioGroup — группа файлов: всё, что начинается с prefix.
type audioGroup struct {
	label  string
	prefix string
}

// groupKeys — в какие группы входит файл: пак («Pack/Voice.USen.pack»), архив BARS внутри него
// («…::Voice/USen/BtlVoice.bars») или просто папка («Sound/Resource/Stream»).
func groupKeys(file string) []string {
	var keys []string
	if outer, _, ok := strings.Cut(file, "::"); ok {
		keys = append(keys, outer)
	}
	if archive, _, ok := strings.Cut(file, "#"); ok {
		keys = append(keys, archive)
	} else if !strings.Contains(file, "::") {
		keys = append(keys, path.Dir(file))
	}
	return keys
}

// groupLabel — коротко для списка: «Voice.USen.pack › BtlVoice.bars».
func groupLabel(key string) string {
	if outer, inner, ok := strings.Cut(key, "::"); ok {
		return path.Base(outer) + " › " + path.Base(inner)
	}
	if strings.HasSuffix(key, ".pack") || strings.Contains(path.Base(key), ".") {
		return path.Base(key) + T(" — весь архив")
	}
	return key + "/"
}

func allGroups() string { return T("Все группы") }

// buildGroups пересчитывает список групп с числом файлов в каждой.
func (u *ui) buildGroups() {
	if u.audioGroup == nil {
		return
	}
	count := map[string]int{}
	if u.session != nil {
		for _, l := range u.session.Audio {
			for _, k := range groupKeys(l.File) {
				count[k]++
			}
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	u.audioGroups = []audioGroup{{allGroups(), ""}}
	labels := []string{allGroups()}
	for _, k := range keys {
		g := audioGroup{fmt.Sprintf("%s (%d)", groupLabel(k), count[k]), k}
		u.audioGroups = append(u.audioGroups, g)
		labels = append(labels, g.label)
	}
	cur := u.audioGroup.Selected
	u.audioGroup.Options = labels
	u.audioGroup.Selected = allGroups()
	for _, l := range labels {
		if strings.SplitN(l, " (", 2)[0] == strings.SplitN(cur, " (", 2)[0] {
			u.audioGroup.Selected = l
		}
	}
	u.audioGroup.Refresh()
}

// groupPrefix — выбранная группа ("" — все).
func (u *ui) groupPrefix() string {
	for _, g := range u.audioGroups {
		if g.label == u.audioGroup.Selected {
			return g.prefix
		}
	}
	return ""
}

func inGroup(file, prefix string) bool {
	if prefix == "" {
		return true
	}
	if !strings.HasPrefix(file, prefix) {
		return false
	}
	rest := file[len(prefix):]
	if strings.HasPrefix(rest, "::") || strings.HasPrefix(rest, "#") {
		return true
	}
	// папка: только файлы прямо в ней
	return strings.HasPrefix(rest, "/") && !strings.ContainsAny(rest[1:], "/#") && !strings.Contains(rest, "::")
}

// audioKinds — фильтр по виду звука.
var audioKinds = []struct {
	label string
	match func(l project.AudioLine) bool
}{
	{"Любые", func(project.AudioLine) bool { return true }},
	{"Похоже на голос", func(l project.AudioLine) bool { return l.Guess == "голос" }},
	{"Похоже на музыку", func(l project.AudioLine) bool { return l.Guess == "музыка" }},
	{"Похоже на эффект или тишину", func(l project.AudioLine) bool { return l.Guess == "эффект" || l.Guess == "тишина" }},
	{"Распознано: с речью", func(l project.AudioLine) bool { return l.Recognized && l.Transcript != "" }},
	{"Распознано: без речи", func(l project.AudioLine) bool { return l.Recognized && l.Transcript == "" }},
	{"Ещё не распознано", func(l project.AudioLine) bool { return !l.Recognized }},
	{"С переводом", func(l project.AudioLine) bool { return l.Translation != "" }},
	{"Озвучено", func(l project.AudioLine) bool { return l.Dub != "" }},
	{"Отмеченные", func(l project.AudioLine) bool { return l.Selected }},
	{"Не отмеченные", func(l project.AudioLine) bool { return !l.Selected }},
}

func audioKindLabels() []string {
	out := make([]string, len(audioKinds))
	for i, k := range audioKinds {
		out[i] = T(k.label)
	}
	return out
}

func (u *ui) kindMatch(l project.AudioLine) bool {
	for _, k := range audioKinds {
		if T(k.label) == u.audioKind.Selected {
			return k.match(l)
		}
	}
	return true
}

// setFound ставит или снимает галочки у всего найденного; flip — обратить.
func (u *ui) setFound(on, flip bool) {
	if u.session == nil {
		return
	}
	for _, i := range u.audioView {
		l := &u.session.Audio[i]
		if flip {
			l.Selected = !l.Selected
		} else {
			l.Selected = on
		}
	}
	u.session.SaveAudio()
	u.fillAudio()
}

// shiftHeld — зажат ли Shift (для отметки диапазоном).
func shiftHeld() bool {
	d, ok := fyne.CurrentApp().Driver().(desktop.Driver)
	return ok && d.CurrentKeyModifiers()&fyne.KeyModifierShift != 0
}

// checkRow — галочка у строки row: с Shift — такое же состояние у всех строк от прошлой нажатой.
func (u *ui) checkRow(row int, on bool) {
	if u.session == nil || row >= len(u.audioView) {
		return
	}
	from, to := row, row
	if shiftHeld() && u.lastChecked >= 0 && u.lastChecked < len(u.audioView) {
		from, to = min(row, u.lastChecked), max(row, u.lastChecked)
	}
	for r := from; r <= to; r++ {
		u.session.Audio[u.audioView[r]].Selected = on
	}
	u.lastChecked = row
	u.session.SaveAudio()
	if from != to {
		u.audioListW.Refresh()
	}
	u.updateAudioCounter()
}

func newKindSelect(onChange func()) *widget.Select {
	s := newSelect(audioKindLabels(), func(string) { onChange() })
	s.Selected = T(audioKinds[0].label)
	return s
}
