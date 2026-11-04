// Package gui — окно программы на Fyne.
package gui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/engine"
	"switch-translator/internal/i18n"
	"switch-translator/internal/keys"
	"switch-translator/internal/nx"
	"switch-translator/internal/project"
	"switch-translator/internal/speech"
	"switch-translator/internal/translate"
)

type ui struct {
	app   fyne.App
	win   fyne.Window
	prefs fyne.Preferences

	keysPath, titleKeysPath *widget.Entry
	keysStatus              *widget.RichText
	keysHelp                fyne.CanvasObject // подсказка «где взять ключи» — видна, пока ключи не подошли
	game                    string            // путь к образу
	extras                  []string          // обновления и DLC
	extrasList              *fyne.Container
	gameForm, gameBox       *fyne.Container // перекладываются, когда меняется список дополнений
	busyWhat                string          // что сейчас делается (для сообщения «подожди»)
	gameName                *widget.Label
	gameDir                 *widget.RichText
	workDirLabel            *widget.RichText
	analyzeBtn              *widget.Button
	progressBox             fyne.CanvasObject
	logMenu                 *fyne.MenuItem

	tabs        *container.AppTabs
	resultTab   *container.TabItem
	speechTab   *container.TabItem // 3. речь из звука и роликов — в текст
	translTab   *container.TabItem // 4. перевод всего текста
	dubTab      *container.TabItem // 5. озвучка и субтитры
	media       *container.AppTabs // звук и ролики игры: один список на вкладки 3 и 5
	audioSub    *container.TabItem
	videoSub    *container.TabItem
	mediaHold   [2]*fyne.Container // куда на вкладках 3 и 5 встаёт media
	summary     *fyne.Container    // итог анализа: строки «вердиктов»
	archiveInfo *widget.Label
	extractInfo *widget.RichText
	groups      []fileGroup
	statsTable  *widget.Table
	textFiles   []engine.FileInfo
	audioFiles  []engine.FileInfo
	textList    *widget.List
	audioList   *widget.List
	resultBtns  []*widget.Button

	textTab
	glossaryUI
	audioTab
	audioEditor
	videoTab
	buildTab

	progress *widget.ProgressBar
	spinner  *widget.ProgressBarInfinite // пока объём работы неизвестен
	status   *widget.Label
	stopBtn  *widget.Button

	logView    *logView
	logOpen    bool // панель лога развёрнута
	logToggle  *widget.Button
	renderLog  func()
	appearance appearance

	session *project.Session
	cancel  context.CancelFunc

	progMu   sync.Mutex
	progLast time.Time
}

// Run открывает окно. game — образ из командной строки: его сразу распакуем и проанализируем.
func Run(game string) {
	prevCrash := setupCrashLog()
	a := app.NewWithID("com.switchtranslator.app")
	u := &ui{app: a, prefs: a.Preferences(), logView: newLogView()}
	u.nowPlaying = -1
	i18n.Set(uiLanguage(u.prefs))
	u.appearance = loadAppearance(u.prefs)
	a.Settings().SetTheme(u.appearance.theme())
	a.SetIcon(fyne.NewStaticResource("icon.png", appIcon))
	u.win = a.NewWindow("Switch Translator")
	u.win.Resize(fyne.NewSize(1100, 780))
	u.build()
	u.win.SetOnClosed(func() {
		if u.cancel != nil {
			u.cancel()
		}
		speech.StopWorkers() // помощники Python с загруженными моделями голосов
		if u.session != nil {
			u.session.Close()
		}
	})
	u.reportPrevCrash(prevCrash)
	u.checkUpdate()
	if game != "" {
		u.setGame(game)
		if u.game != "" {
			u.analyze()
		}
	}
	u.win.ShowAndRun()
}

func (u *ui) build() {
	u.tabs = container.NewAppTabs(
		container.NewTabItemWithIcon(T("1. Игра"), theme.FolderOpenIcon(), u.buildGameTab()),
	)
	u.resultTab = container.NewTabItemWithIcon(T("2. Что нашлось"), theme.SearchIcon(), u.buildResultTab())
	// Вкладки идут по этапам работы, а не по видам файлов: речь из звука и роликов становится текстом,
	// весь текст вместе (реплики и речь) переводится по общему словарю, перевод речи озвучивается,
	// всё собирается. Вкладка перевода строится первой: на ней выбор языков, их читают остальные.
	u.translTab = container.NewTabItemWithIcon(T("4. Перевод"), theme.DocumentIcon(), u.buildTextTab())
	audioRec, audioDub := u.buildAudioTab()
	videoRec, videoDub := u.buildVideoTab()
	u.audioSub = container.NewTabItemWithIcon(T("Звук"), theme.VolumeUpIcon(), u.audioPane)
	u.videoSub = container.NewTabItemWithIcon(T("Ролики"), theme.MediaVideoIcon(), u.videoPane)
	u.media = container.NewAppTabs(u.audioSub, u.videoSub)
	u.mediaHold = [2]*fyne.Container{container.NewStack(u.media), container.NewStack()}
	stage := func(intro string, side []fyne.CanvasObject, next *widget.Button, hold *fyne.Container) fyne.CanvasObject {
		next.Importance = widget.LowImportance
		box := container.NewVBox(muted(intro))
		box.Objects = append(box.Objects, side...)
		box.Add(widget.NewSeparator())
		box.Add(container.NewHBox(next))
		return container.NewHSplit(container.NewVScroll(box), hold)
	}
	u.speechTab = container.NewTabItemWithIcon(T("3. Речь в текст"), theme.SearchIcon(), stage(
		T("Здесь речь из звука и роликов игры становится текстом. Переводится она на следующей вкладке — вместе со всем ")+
			T("текстом игры и по общему словарю. Справа — звук и ролики: послушай и отметь, где есть речь."),
		append(audioRec, videoRec...),
		widget.NewButtonWithIcon(T("Дальше — перевод"), theme.NavigateNextIcon(), func() { u.tabs.Select(u.translTab) }),
		u.mediaHold[0]))
	u.dubTab = container.NewTabItemWithIcon(T("5. Озвучка"), theme.VolumeUpIcon(), stage(
		T("Перевод готов — теперь его можно озвучить и вшить в ролики субтитрами. Если игре нужен только текст, ")+
			T("этот шаг можно пропустить."),
		append(audioDub, videoDub...),
		widget.NewButtonWithIcon(T("Дальше — сборка"), theme.NavigateNextIcon(), func() { u.tabs.Select(u.buildTabItem) }),
		u.mediaHold[1]))
	u.tabs.Append(u.resultTab)
	u.tabs.Append(u.speechTab)
	u.tabs.Append(u.translTab)
	u.tabs.Append(u.dubTab)
	u.buildTabItem = container.NewTabItemWithIcon(T("6. Сборка"), theme.DownloadIcon(), u.buildBuildTab())
	u.tabs.Append(u.buildTabItem)
	u.tabs.OnSelected = func(t *container.TabItem) {
		switch t {
		case u.speechTab:
			u.placeMedia(0)
		case u.dubTab:
			u.placeMedia(1)
		}
		if t == u.buildTabItem {
			u.updateBuildInfo() // перевод и озвучка могли измениться на других вкладках
		}
		if t == u.translTab && u.session != nil {
			u.session.CountWords() // распознанная речь — тоже текст
			u.updateCounter()
			u.updateGlossaryInfo()
		}
	}

	u.progress = widget.NewProgressBar()
	u.status = widget.NewLabel("")
	u.status.Truncation = fyne.TextTruncateEllipsis
	u.stopBtn = widget.NewButtonWithIcon(T("Остановить"), theme.MediaStopIcon(), func() {
		if u.cancel != nil {
			u.cancel()
		}
	})
	u.stopBtn.Disable()
	// Прогресс показывается только во время работы.
	u.spinner = widget.NewProgressBarInfinite()
	u.spinner.Stop()
	u.progressBox = container.NewVBox(widget.NewSeparator(),
		container.NewBorder(nil, nil, nil, u.stopBtn, container.NewVBox(u.status, container.NewStack(u.progress, u.spinner))))
	u.progressBox.Hide()
	u.withLog(container.NewBorder(nil, u.progressBox, nil, nil, u.tabs))
	u.win.SetMainMenu(u.mainMenu())

	u.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) { u.dropped(uris) })
	u.setResultsEnabled(false)
	u.loadKeys(false)
	u.setGame(u.prefs.String("game"))
}

// placeMedia ставит список звука и роликов на вкладку этапа i (0 — «Речь в текст», 1 — «Озвучка»).
func (u *ui) placeMedia(i int) {
	for k, h := range u.mediaHold {
		if k == i {
			h.Objects = []fyne.CanvasObject{u.media}
		} else {
			h.Objects = nil
		}
		h.Refresh()
	}
}

// updateMediaTabs — сколько звуков и роликов, в заголовках списка.
func (u *ui) updateMediaTabs() {
	if u.media == nil {
		return
	}
	a, v := 0, 0
	if u.session != nil {
		a, v = len(u.session.Audio), len(u.session.Video)
	}
	u.audioSub.Text = fmt.Sprintf(T("Звук (%d)"), a)
	u.videoSub.Text = fmt.Sprintf(T("Ролики (%d)"), v)
	u.media.Refresh()
}

// onVideoSub — открыт ли в списке звука и роликов список роликов.
func (u *ui) onVideoSub() bool { return u.media.Selected() == u.videoSub }

func shortcut(key fyne.KeyName) fyne.Shortcut {
	return &desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierShortcutDefault}
}

// mainMenu — все команды программы. На macOS это строка меню, на Windows — меню в окне.
func (u *ui) mainMenu() *fyne.MainMenu {
	item := func(label string, key fyne.KeyName, fn func()) *fyne.MenuItem {
		it := fyne.NewMenuItem(label, fn)
		if key != "" {
			it.Shortcut = shortcut(key)
			if runtime.GOOS != "darwin" { // на macOS сочетания обрабатывает само меню
				u.win.Canvas().AddShortcut(it.Shortcut, func(fyne.Shortcut) { fn() })
			}
		}
		return it
	}
	ifSession := func(fn func()) func() {
		return func() {
			if u.session != nil && u.cancel == nil {
				fn()
			}
		}
	}
	// Fyne переносит пункт с меткой «Settings…» в меню программы на macOS — рядом с «Hide» и «Quit».
	settingsLabel := T("Настройки…")
	if runtime.GOOS == "darwin" {
		settingsLabel = "Settings…"
	}
	file := fyne.NewMenu(T("Файл"),
		item(T("Выбрать образ игры…"), fyne.KeyO, u.chooseGame),
		item(T("Выбрать ключи…"), "", u.chooseKeys),
		item(T("Найти ключи на карте памяти"), "", u.findKeys),
		fyne.NewMenuItemSeparator(),
		item(T("Распаковать и проанализировать"), fyne.KeyReturn, u.mainAction),
		item(T("Извлечь текст, звук и шрифты"), "", ifSession(u.extractUseful)),
		item(T("Извлечь все файлы игры"), "", ifSession(u.extractAll)),
		item(revealLabel(), "", ifSession(u.openWorkDir)),
		fyne.NewMenuItemSeparator(),
		item(settingsLabel, fyne.KeyComma, u.showSettings),
	)
	u.logMenu = item(T("Показать лог"), fyne.KeyL, func() { u.setLogOpen(!u.logOpen) })
	view := fyne.NewMenu(T("Вид"),
		item(T("Игра"), fyne.Key1, func() { u.tabs.SelectIndex(0) }),
		item(T("Что нашлось"), fyne.Key2, func() { u.tabs.SelectIndex(1) }),
		item(T("Речь в текст"), fyne.Key3, func() { u.tabs.SelectIndex(2) }),
		item(T("Перевод"), fyne.Key4, func() { u.tabs.SelectIndex(3) }),
		item(T("Озвучка"), fyne.Key5, func() { u.tabs.SelectIndex(4) }),
		item(T("Сборка"), fyne.Key6, func() { u.tabs.SelectIndex(5) }),
		fyne.NewMenuItemSeparator(),
		u.logMenu,
	)
	help := fyne.NewMenu(T("Справка"),
		item(T("Где взять ключи…"), "", u.showKeysHelp),
		fyne.NewMenuItemSeparator(),
		item(T("Проверить обновления…"), "", u.checkUpdateNow),
		item(T("Поддержать проект ♥…"), "", u.showDonate),
		item(T("О программе"), "", u.showAbout),
	)
	return fyne.NewMainMenu(file, view, help)
}

// nextFromResults — следующий шаг после анализа: есть звук или ролики — сначала речь в текст, иначе сразу перевод.
func (u *ui) nextFromResults() {
	switch {
	case len(u.session.Audio) > 0:
		u.media.Select(u.audioSub)
		u.tabs.Select(u.speechTab)
	case len(u.session.Video) > 0:
		u.media.Select(u.videoSub)
		u.tabs.Select(u.speechTab)
	default:
		u.tabs.Select(u.translTab)
	}
}

// mainAction — главное действие текущей вкладки (Cmd/Ctrl+Return).
func (u *ui) mainAction() {
	if u.cancel != nil {
		return
	}
	switch u.tabs.SelectedIndex() {
	case 0:
		u.analyze()
	case 1:
		if u.session != nil {
			u.nextFromResults()
		}
	case 2:
		if u.session != nil && u.onVideoSub() {
			u.recognizeVideo()
		} else if u.session != nil {
			u.recognize()
		}
	case 3:
		if u.session != nil {
			u.translateText()
		}
	case 4:
		if u.session != nil && u.onVideoSub() {
			u.renderVideo()
		} else if u.session != nil {
			u.dub()
		}
	case 5:
		if u.session != nil {
			u.buildMod()
		}
	}
}

// dropped: образ игры или файлы ключей, перетащенные на окно.
func (u *ui) dropped(uris []fyne.URI) {
	// сначала сама игра — она подбирает дополнения рядом с собой и сбросила бы перетащенные вместе с ней
	isExtra := func(uri fyne.URI) bool { return project.IsExtraName(strings.ToLower(filepath.Base(uri.Path()))) }
	uris = append([]fyne.URI(nil), uris...)
	sort.SliceStable(uris, func(i, j int) bool { return !isExtra(uris[i]) && isExtra(uris[j]) })
	var extras []string
	defer func() {
		if len(extras) > 0 {
			u.addExtras(extras)
			u.tabs.SelectIndex(0)
		}
	}()
	for _, uri := range uris {
		p := uri.Path()
		name := strings.ToLower(filepath.Base(p))
		switch {
		case (strings.HasSuffix(name, ".nsp") || strings.HasSuffix(name, ".nsz")) && project.IsExtraName(name):
			extras = append(extras, p)
		case strings.HasSuffix(name, ".nsp") || strings.HasSuffix(name, ".nsz"):
			u.setGame(p)
			u.tabs.SelectIndex(0)
		case strings.Contains(name, "prod") && strings.HasSuffix(name, ".keys"):
			u.setKeys(p)
		case strings.Contains(name, "title") && strings.HasSuffix(name, ".keys"):
			u.titleKeysPath.SetText(p)
			u.loadKeys(true)
		default:
			dialog.ShowInformation(T("Этот файл не подходит"),
				T("Сюда можно перетащить образ игры (.nsp, .nsz) или файлы ключей (prod.keys, title.keys)."), u.win)
		}
	}
}

// ---------- вкладка 1: ключи и образ ----------

func (u *ui) buildGameTab() fyne.CanvasObject {
	u.keysPath = widget.NewEntry()
	u.keysPath.SetPlaceHolder("prod.keys")
	u.keysPath.OnSubmitted = func(p string) { u.setKeys(p) }
	u.titleKeysPath = widget.NewEntry()
	u.titleKeysPath.SetPlaceHolder(T("необязательно: нужен, если в образе нет тикета"))
	u.titleKeysPath.OnSubmitted = func(string) { u.loadKeys(true) }
	u.titleKeysPath.SetText(u.prefs.String("titleKeys"))
	u.keysPath.SetText(u.prefs.String("prodKeys"))
	if u.keysPath.Text == "" || !fileExists(u.keysPath.Text) {
		if found := keys.Find(); len(found) > 0 {
			u.keysPath.SetText(found[0])
			if u.titleKeysPath.Text == "" {
				u.titleKeysPath.SetText(keys.TitleKeysNear(found[0]))
			}
		}
	}
	u.keysStatus = widget.NewRichText()
	u.keysStatus.Wrapping = fyne.TextWrapWord

	var prodBtn, titleBtn, gameBtn *widget.Button
	prodBtn = widget.NewButtonWithIcon(T("Выбрать…"), theme.FolderOpenIcon(), func() { u.chooseKeysWith(prodBtn) })
	titleBtn = widget.NewButtonWithIcon(T("Выбрать…"), theme.FolderOpenIcon(), func() {
		u.pick(pickRequest{kind: pickFile, title: T("Выбери title.keys"), current: u.titleKeysPath.Text, btn: titleBtn, done: func(p string) {
			u.titleKeysPath.SetText(p)
			u.loadKeys(true)
		}})
	})
	findBtn := widget.NewButtonWithIcon(T("Найти"), theme.SearchIcon(), u.findKeys)
	gameBtn = widget.NewButtonWithIcon(T("Выбрать…"), theme.FolderOpenIcon(), func() { u.chooseGameWith(gameBtn) })

	u.gameName = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	u.gameName.Truncation = fyne.TextTruncateEllipsis
	u.gameDir = widget.NewRichText()
	u.gameDir.Truncation = fyne.TextTruncateEllipsis
	u.workDirLabel = widget.NewRichText()
	u.workDirLabel.Wrapping = fyne.TextWrapWord
	gameInfo := container.New(layout.NewCustomPaddedVBoxLayout(-theme.Padding()*2), u.gameName, u.gameDir)

	var addBtn *widget.Button
	addBtn = widget.NewButtonWithIcon(T("Добавить…"), theme.ContentAddIcon(), func() {
		cur := u.game
		if len(u.extras) > 0 {
			cur = u.extras[len(u.extras)-1]
		}
		u.pick(pickRequest{kind: pickFile, title: T("Выбери обновления и DLC (можно сразу несколько)"), current: cur,
			patterns: []string{"*.nsp", "*.nsz"}, btn: addBtn, doneAll: u.addExtras})
	})
	u.extrasList = container.NewVBox()

	form := newForm(
		T("Ключи"), container.NewBorder(nil, nil, nil, container.NewHBox(prodBtn, findBtn), u.keysPath),
		"title.keys", container.NewBorder(nil, nil, nil, titleBtn, u.titleKeysPath),
		"", u.keysStatus,
		T("Образ игры"), container.NewBorder(nil, nil, nil, gameBtn, gameInfo),
		T("Обновления и DLC"), container.NewBorder(nil, nil, nil, container.NewVBox(addBtn), u.extrasList),
		"", u.workDirLabel,
	)
	u.analyzeBtn = widget.NewButtonWithIcon(T("Распаковать и проанализировать"), theme.MediaPlayIcon(), u.analyze)
	u.analyzeBtn.Importance = widget.HighImportance

	help := widget.NewRichTextFromMarkdown(keysHelpShort())
	help.Wrapping = fyne.TextWrapWord
	more := widget.NewButton(T("Где взять ключи…"), u.showKeysHelp)
	more.Importance = widget.LowImportance
	u.keysHelp = container.NewVBox(widget.NewSeparator(), help, container.NewHBox(more))

	u.gameForm = form
	u.gameBox = container.NewVBox(
		form,
		container.NewHBox(u.analyzeBtn),
		u.keysHelp,
		widget.NewSeparator(),
		muted(T("Программа расшифровывает образ сама, внешние утилиты не нужны. ")+
			T("Обновления и DLC, лежащие рядом с образом, находятся сами. Обновление нужно, чтобы перевод совпал с версией игры ")+
			T("на консоли: оно меняет и добавляет тексты. Из нескольких обновлений берётся самое новое, тексты DLC переводятся вместе с игрой. ")+
			T("Образ и ключи можно перетащить прямо в окно.")),
	)
	return container.NewVScroll(u.gameBox)
}

func (u *ui) chooseGame() { u.chooseGameWith(nil) }
func (u *ui) chooseKeys() { u.chooseKeysWith(nil) }
func (u *ui) chooseGameWith(b *widget.Button) {
	var btn fyne.Disableable
	if b != nil {
		btn = b
	}
	u.pick(pickRequest{kind: pickFile, title: T("Выбери образ игры"), current: u.game,
		patterns: []string{"*.nsp", "*.nsz"}, btn: btn, done: func(p string) {
			u.setGame(p)
			u.tabs.SelectIndex(0)
		}})
}

func (u *ui) chooseKeysWith(b *widget.Button) {
	var btn fyne.Disableable
	if b != nil {
		btn = b
	}
	u.pick(pickRequest{kind: pickFile, title: T("Выбери prod.keys"), current: u.keysPath.Text, btn: btn, done: u.setKeys})
}

func (u *ui) setKeys(p string) {
	u.keysPath.SetText(p)
	if u.titleKeysPath.Text == "" {
		u.titleKeysPath.SetText(keys.TitleKeysNear(p))
	}
	u.loadKeys(true)
}

// setGame показывает выбранный образ: имя крупно, папку — мелко.
func (u *ui) setGame(p string) {
	p = strings.TrimSpace(p)
	if !fileExists(p) {
		u.game = ""
		u.gameName.SetText(T("Образ не выбран"))
		setMuted(u.gameDir, T("Выбери .nsp или .nsz — или перетащи файл в окно"))
		u.workDirLabel.Hide()
		u.setExtras(nil)
		return
	}
	u.game = p
	u.prefs.SetString("game", p)
	u.gameName.SetText(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
	setMuted(u.gameDir, filepath.Dir(p))
	setMuted(u.workDirLabel, T("Результаты сохранятся рядом с образом, в папке «")+filepath.Base(project.DefaultWorkDir(p))+"».")
	u.workDirLabel.Show()
	u.setExtras(project.FindExtras(p))
}

// addExtras добавляет обновления и DLC (повторно один и тот же файл не добавляется).
func (u *ui) addExtras(paths []string) {
	list := append([]string(nil), u.extras...)
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" && !slices.Contains(list, p) {
			list = append(list, p)
		}
	}
	u.setExtras(list)
}

// setExtras показывает список обновлений и DLC: что это и кнопка «убрать».
func (u *ui) setExtras(list []string) {
	u.extras = list
	u.extrasList.RemoveAll()
	defer func() {
		if u.gameBox != nil {
			u.gameForm.Refresh()
			u.gameBox.Refresh()
		}
	}()
	if len(list) == 0 {
		info := widget.NewRichText()
		info.Wrapping = fyne.TextWrapWord
		setMuted(info, T("Нет. Если на консоли игра обновлена или куплены DLC, добавь их файлы (title id …800 — обновление, …001 и дальше — DLC)."))
		u.extrasList.Add(info)
		return
	}
	for _, p := range list {
		kind := "DLC"
		if project.IsUpdateName(filepath.Base(p)) {
			kind = T("Обновление")
		}
		name := widget.NewLabel(kind + ": " + strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		name.Truncation = fyne.TextTruncateEllipsis
		rm := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
			var rest []string
			for _, x := range u.extras {
				if x != p {
					rest = append(rest, x)
				}
			}
			u.setExtras(rest)
		})
		rm.Importance = widget.LowImportance
		u.extrasList.Add(container.NewBorder(nil, nil, nil, rm, name))
	}
}

func setMuted(rt *widget.RichText, text string) {
	rt.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder}}}
	rt.Refresh()
}

// findKeys ищет ключи в ~/.switch и на подключённой карте памяти.
func (u *ui) findKeys() {
	found := keys.Find()
	switch len(found) {
	case 0:
		dialog.ShowInformation(T("Ключи не найдены"),
			T("Ни в ~/.switch, ни на подключённой карте памяти (папка switch) нет prod.keys.\n")+
				T("Вставь карту памяти приставки в компьютер или выбери файл вручную."), u.win)
	case 1:
		u.titleKeysPath.SetText("")
		u.setKeys(found[0])
		u.logf("Ключи найдены: %s", found[0])
	default:
		sel := widget.NewRadioGroup(found, nil)
		sel.Required = true
		sel.SetSelected(found[0])
		dialog.ShowCustomConfirm(T("Нашлось несколько prod.keys"), T("Взять"), T("Отмена"), sel, func(ok bool) {
			if ok {
				u.titleKeysPath.SetText("")
				u.setKeys(sel.Selected)
			}
		}, u.win)
	}
}

// loadKeys проверяет ключи и пишет итог простыми словами; подсказка про ключи видна, пока они не подошли.
func (u *ui) loadKeys(showErr bool) *keys.Keyset {
	status := func(ok bool, text, note string) {
		segs := []widget.RichTextSegment{}
		if ok {
			segs = append(segs, &widget.TextSegment{Text: "✓ " + text, Style: widget.RichTextStyle{ColorName: theme.ColorNameSuccess, Inline: true, TextStyle: fyne.TextStyle{Bold: true}}})
		} else {
			segs = append(segs, &widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: theme.ColorNameError, Inline: true}})
		}
		if note != "" {
			segs = append(segs, &widget.TextSegment{Text: "  " + note, Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder, Inline: true}})
		}
		u.keysStatus.Segments = segs
		u.keysStatus.Refresh()
		setVisible(u.keysHelp, !ok)
	}
	p := strings.TrimSpace(u.keysPath.Text)
	if p == "" {
		status(false, T("Ключи не выбраны: нажми «Найти» или «Выбрать…»."), "")
		return nil
	}
	ks, err := keys.Load(p)
	if err != nil {
		status(false, T("Эти ключи не подходят: ")+i18n.Err(err), "")
		if showErr {
			u.showError(err)
		}
		return nil
	}
	u.prefs.SetString("prodKeys", p)
	note := ""
	if tp := strings.TrimSpace(u.titleKeysPath.Text); tp != "" {
		if err := ks.LoadTitleKeys(tp); err != nil {
			status(true, T("Ключи подходят"), T("title.keys не прочитался: ")+i18n.Err(err))
			return ks
		}
		u.prefs.SetString("titleKeys", tp)
		if n := len(ks.TitleKeys); n > 0 {
			note = fmt.Sprintf(T("(и ключи %d купленных игр)"), n)
		}
	}
	status(true, T("Ключи подходят"), note)
	return ks
}

func (u *ui) analyze() {
	ks := u.loadKeys(true)
	if ks == nil {
		return
	}
	game := u.game
	if game == "" {
		dialog.ShowInformation(T("Образ не выбран"), T("Выбери образ игры (.nsp или .nsz) или перетащи его в окно."), u.win)
		return
	}
	workDir := project.DefaultWorkDir(game)
	extras := append([]string(nil), u.extras...)

	if u.session != nil {
		u.session.Close()
		u.session = nil
	}
	u.setResultsEnabled(false)
	u.run(T("Анализ игры"), false, func(ctx context.Context) (func(), error) {
		s, err := project.Analyze(game, extras, ks, workDir, u.logf, u.progressFn)
		if err != nil {
			return nil, err
		}
		return func() {
			u.session = s
			u.showReport()
			u.tabs.Select(u.resultTab)
		}, nil
	})
}

// ---------- вкладка 2: результаты анализа ----------

func (u *ui) buildResultTab() fyne.CanvasObject {
	u.summary = container.NewVBox(muted(T("Здесь появится итог, когда игра будет распакована на первой вкладке.")))

	headers := []string{T("Что это"), T("Файлов"), T("Размер"), T("Форматы")}
	u.statsTable = widget.NewTableWithHeaders(
		func() (int, int) { return len(u.groups), 4 },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			g := u.groups[id.Row]
			o.(*widget.Label).SetText([]string{g.title, strconv.Itoa(g.count), nx.HumanSize(g.size), g.formats}[id.Col])
		})
	u.statsTable.ShowHeaderColumn = false
	u.statsTable.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	u.statsTable.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 {
			o.(*widget.Label).SetText(headers[id.Col])
		}
	}
	for i, w := range []float32{330, 80, 90, 520} {
		u.statsTable.SetColumnWidth(i, w)
	}
	u.archiveInfo = widget.NewLabel("")
	u.archiveInfo.Wrapping = fyne.TextWrapWord

	fileList := func(files *[]engine.FileInfo) *widget.List {
		return widget.NewList(
			func() int { return len(*files) },
			func() fyne.CanvasObject {
				name := widget.NewLabel("")
				name.Truncation = fyne.TextTruncateEllipsis
				size := widget.NewLabel(T("1023 МБ"))
				size.Alignment = fyne.TextAlignTrailing
				return container.NewBorder(nil, nil, nil, size, name)
			},
			func(i widget.ListItemID, o fyne.CanvasObject) {
				f := (*files)[i]
				c := o.(*fyne.Container)
				c.Objects[0].(*widget.Label).SetText(f.Path + "  —  " + i18n.Phrases(f.Format))
				c.Objects[1].(*widget.Label).SetText(nx.HumanSize(f.Size))
			})
	}
	u.textList = fileList(&u.textFiles)
	u.audioList = fileList(&u.audioFiles)

	extractTA := widget.NewButtonWithIcon(T("Извлечь текст, звук и шрифты"), theme.DownloadIcon(), u.extractUseful)
	extractAll := widget.NewButtonWithIcon(T("Извлечь все файлы игры"), theme.DownloadIcon(), u.extractAll)
	openDir := widget.NewButtonWithIcon(revealLabel(), theme.FolderOpenIcon(), u.openWorkDir)
	next := widget.NewButtonWithIcon(T("Дальше"), theme.NavigateNextIcon(), u.nextFromResults)
	next.Importance = widget.HighImportance
	u.resultBtns = []*widget.Button{extractTA, extractAll, openDir, next}

	u.extractInfo = widget.NewRichTextFromMarkdown("")
	u.extractInfo.Wrapping = fyne.TextWrapWord
	extract := widget.NewAccordion(widget.NewAccordionItem(T("Для опытных: извлечь файлы игры"), container.NewVBox(
		u.extractInfo,
		container.NewHBox(extractTA, extractAll),
		widget.NewRichTextFromMarkdown(T("**Папка результатов** лежит рядом с образом игры. В ней:\n\n")+
			T("- `strings.json` — все найденные реплики игры;\n")+
			T("- `strings.ru.json` (или другой язык) — перевод, сохраняется по ходу работы;\n")+
			T("- `audio.json` и папка `audio` — распознанная речь, перевод и озвучка;\n")+
			T("- `report.json` — полный список файлов игры;\n")+
			T("- `romfs` — извлечённые файлы, если ты их извлекал.")),
		container.NewHBox(openDir),
	)))

	allFiles := container.NewBorder(u.archiveInfo, extract, nil, nil, u.statsTable)
	details := container.NewAppTabs(
		container.NewTabItem(T("Текст"), u.textList),
		container.NewTabItem(T("Звук"), u.audioList),
		container.NewTabItem(T("Все файлы"), allFiles),
	)
	// язык оригинала выбирается сразу после анализа: от него зависят строки для перевода, озвучка в архивах
	// этого языка (Voice.USen.pack, Voice.JPja.pack…) и язык, на котором распознаётся речь
	u.localeSel = newSelect(nil, func(sel string) { u.onLocale(sel) })
	u.localeSel.Hide()
	u.fromSel = newSelect(langOptions(), func(string) {
		u.prefs.SetString("from", langCode(u.fromSel.Selected))
		if u.session != nil && u.session.Locale == "" {
			// в игре нет версий текста Nintendo: язык выбирает банк озвучки (Dialogues_ENG, Dialogues_FRA…)
			u.session.SelectLang(langCode(u.fromSel.Selected))
			u.fillAudio()
		}
		u.updateFromInfo()
	})
	selectByCode(u.fromSel, u.prefs.StringWithFallback("from", "ja"))
	langRow := container.NewBorder(nil, nil,
		widget.NewLabelWithStyle(T("Язык оригинала"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), next,
		container.NewStack(u.localeSel, u.fromSel))
	langNote := muted(T("С какой языковой версии игры переводить: от неё зависят строки текста, файлы речи этого языка ") +
		T("на «Речи в текст» и язык, на котором распознаётся речь."))
	top := container.NewBorder(nil, container.NewVBox(langNote, langRow), nil, nil, container.NewVScroll(u.summary))
	bottom := container.NewBorder(section(T("Подробности")), nil, nil, nil, details)
	split := container.NewVSplit(top, bottom)
	split.Offset = 0.55
	return split
}

func revealLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return T("Показать в Finder")
	case "windows":
		return T("Показать в Проводнике")
	}
	return T("Открыть папку результатов")
}

func (u *ui) openWorkDir() {
	if u.session != nil {
		u.app.OpenURL(&url.URL{Scheme: "file", Path: u.session.WorkDir})
	}
}

func (u *ui) extractUseful() {
	u.extract([]engine.Category{engine.Text, engine.Audio, engine.Font})
}

func (u *ui) extractAll() { u.extract(nil) }

// verdict — одна строка итога: значок, короткий вывод и пояснение простыми словами.
func verdict(icon fyne.Resource, title, text string) fyne.CanvasObject {
	t := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	d := widget.NewLabel(text)
	d.Wrapping = fyne.TextWrapWord
	tight := layout.NewCustomPaddedVBoxLayout(-theme.Padding() * 2) // заголовок и пояснение — одна мысль
	return container.NewBorder(nil, nil, container.NewVBox(widget.NewIcon(icon)), nil, container.New(tight, t, d))
}

// fileGroup — строка таблицы «Все файлы»: файлы одного назначения.
type fileGroup struct {
	title   string
	count   int
	size    int64
	formats string
}

var groupTitles = map[engine.Category]string{
	engine.Text:       "Тексты и диалоги",
	engine.Audio:      "Музыка и звук",
	engine.Font:       "Шрифты",
	engine.Video:      "Видео",
	engine.Archive:    "Архивы: уровни, модели, интерфейс",
	engine.Graphics:   "Модели и текстуры",
	engine.Service:    "Шейдеры, эффекты, настройки — переводить не нужно",
	engine.Compressed: "Сжатые файлы",
	engine.Unknown:    "Прочие форматы — для перевода не нужны",
}

func fileGroups(r *engine.Report) []fileGroup {
	byCat := map[engine.Category]*fileGroup{}
	formats := map[engine.Category][]string{}
	var order []engine.Category
	for _, st := range r.Stats() { // Stats уже упорядочены: сначала важное для перевода
		g, ok := byCat[st.Category]
		if !ok {
			g = &fileGroup{title: T(groupTitles[st.Category])}
			byCat[st.Category] = g
			order = append(order, st.Category)
		}
		g.count += st.Count
		g.size += st.Size
		formats[st.Category] = append(formats[st.Category], i18n.Phrases(st.Format))
	}
	out := make([]fileGroup, 0, len(order))
	for _, c := range order {
		g := byCat[c]
		g.formats = strings.Join(formats[c], ", ")
		out = append(out, *g)
	}
	return out
}

func (u *ui) showReport() {
	s := u.session
	r := s.Report
	game := strings.TrimSuffix(filepath.Base(s.Game.Path), filepath.Ext(s.Game.Path))

	var gameEngines, audioEngines []string
	for _, e := range r.Engines {
		switch e.Name {
		case "Wwise", "FMOD", "CRIWARE":
			audioEngines = append(audioEngines, e.Name)
		default:
			gameEngines = append(gameEngines, e.Name)
		}
	}
	engineName := strings.Join(gameEngines, ", ")
	if strings.Contains(engineName, "Nintendo") {
		engineName = "Nintendo"
	}
	head := game + T(" · движок не определён")
	switch {
	case engineName != "" && len(s.All) > 0:
		head = game + T(" · движок ") + engineName + T(", поддерживается")
	case engineName != "":
		head = game + T(" · движок ") + engineName + T(" — пока не поддерживается")
	}
	rows := []fyne.CanvasObject{widget.NewLabelWithStyle(head, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})}
	var ready []readyItem

	// Текст
	switch {
	case len(s.All) > 0:
		locs := s.Locales()
		text := fmt.Sprintf(T("Найдено %d реплик."), len(s.All))
		if len(locs) > 1 {
			var names []string
			seen := map[string]bool{}
			hasRu := ""
			for _, l := range locs {
				if !seen[l.Lang] {
					seen[l.Lang] = true
					names = append(names, langName(l.Lang))
				}
				if l.Lang == "ru" {
					hasRu = l.Code
				}
			}
			per := 0
			for _, l := range locs {
				if l.Code == s.DefaultLocale("en") {
					per = l.Count
				}
			}
			text = fmt.Sprintf(T("В игре %d версий текста по %d реплик: %s. Переводить нужно с одной из них — выбери её внизу, в «Языке оригинала»."),
				len(locs), per, strings.Join(names, ", "))
			if hasRu != "" {
				text += T(" Русский в игре уже есть (") + hasRu + ")."
			} else {
				text += T(" Русского среди них нет.")
			}
		}
		rows = append(rows, verdict(theme.ConfirmIcon(), T("Текст можно переводить"), text))
		ready = append(ready, readyItem{theme.ConfirmIcon(), T("текст можно переводить")})
	case len(gameEngines) > 0:
		rows = append(rows, verdict(theme.WarningIcon(), T("Текст: доставать реплики из этого движка программа пока не умеет"),
			T("Файлы можно извлечь и перевести в других программах — раздел «Для опытных» во вкладке «Все файлы» ниже.")))
		ready = append(ready, readyItem{theme.WarningIcon(), T("текст не извлечён")})
	default:
		rows = append(rows, verdict(theme.WarningIcon(), T("Текст: знакомых форматов не нашлось"),
			T("Вкладка «Все файлы» ниже показывает, из чего состоит игра.")))
		ready = append(ready, readyItem{theme.WarningIcon(), T("текст не найден")})
	}

	// Шрифты — сразу после текста: без кириллицы перевод не отобразится
	fonts := len(r.Of(engine.Font))
	for ext, n := range r.ArchiveContents() {
		if strings.HasPrefix(ext, ".bffnt") || strings.HasPrefix(ext, ".bfttf") || strings.HasPrefix(ext, ".bfotf") {
			fonts += n
		}
	}
	if fonts > 0 {
		rows = append(rows, verdict(theme.WarningIcon(), fmt.Sprintf(T("Шрифты: %d — нужна кириллица"), fonts),
			T("Чтобы русский текст отображался в самой игре, в её шрифте должна быть кириллица. Это понадобится позже, при сборке перевода обратно в игру.")))
		ready = append(ready, readyItem{theme.WarningIcon(), T("шрифтам нужна кириллица")})
	}

	// Звук
	audio := r.Of(engine.Audio)
	var streams []engine.FileInfo
	var streamSize int64
	archives := map[string]int{} // архивы со множеством звуков: BFSAR, банки FMOD и Wwise
	for _, f := range audio {
		switch {
		case strings.HasPrefix(f.Format, "BFSAR"):
			archives["BFSAR"]++
		case strings.HasPrefix(f.Format, "FMOD"):
			archives["FMOD"]++
		case strings.HasPrefix(f.Format, "Wwise SoundBank"):
			archives["Wwise"]++
		}
		streams = append(streams, f)
		streamSize += f.Size
	}
	inArchives := 0 // архивы разобраны на звуки — считаем звуки, а не архивы
	for _, l := range s.Audio {
		if strings.HasPrefix(l.Format, "звук в ") {
			inArchives++
		}
	}
	extra := ""
	if len(audioEngines) > 0 {
		extra += T(" Звуковой движок: ") + strings.Join(audioEngines, ", ") + "."
	}
	switch {
	case len(streams) == 0 || len(s.Audio) == 0:
		rows = append(rows, verdict(theme.InfoIcon(), T("Озвучки не нашлось"),
			T("Звуковых файлов в знакомых форматах нет — переводить голосом нечего.")+extra))
		ready = append(ready, readyItem{theme.InfoIcon(), T("озвучки нет")})
	case inArchives > 0:
		var where []string
		for _, k := range []string{"BFSAR", "FMOD", "Wwise"} {
			if n := archives[k]; n > 0 {
				where = append(where, fmt.Sprintf("%s: %d", map[string]string{"BFSAR": T("архивов BFSAR"), "FMOD": T("банков FMOD"), "Wwise": T("банков Wwise")}[k], n))
			}
		}
		rows = append(rows, verdict(theme.ConfirmIcon(),
			fmt.Sprintf(T("Звук: %d звуков — похоже, есть озвучка"), len(s.Audio)),
			fmt.Sprintf(T("Из них %d — внутри звуковых архивов (%s): они разобраны на отдельные реплики и эффекты; "), inArchives, strings.Join(where, ", "))+
				T("из озвучки на разных языках в списке только язык оригинала. ")+
				T("Речь распознаётся на вкладке «Речь в текст», переводится вместе с текстом и озвучивается на вкладке «Озвучка».")+extra))
		ready = append(ready, readyItem{theme.ConfirmIcon(), T("есть озвучка")})
	case len(streams) < 200 && streamSize/int64(len(streams)) > 1<<20:
		rows = append(rows, verdict(theme.InfoIcon(),
			fmt.Sprintf(T("Звук: %d дорожек, %s — скорее всего, музыка"), len(streams), nx.HumanSize(streamSize)),
			fmt.Sprintf(T("Дорожки крупные (в среднем %s), а озвучку обычно хранят сотнями маленьких файлов. ")+
				T("Послушать дорожки и проверить речь можно на вкладке «Речь в текст»."), nx.HumanSize(streamSize/int64(len(streams))))+extra))
		ready = append(ready, readyItem{theme.InfoIcon(), T("звук — скорее всего музыка")})
	default:
		rows = append(rows, verdict(theme.ConfirmIcon(),
			fmt.Sprintf(T("Звук: %d файлов, %s — похоже, есть озвучка"), len(streams), nx.HumanSize(streamSize)),
			T("Речь распознаётся на вкладке «Речь в текст», переводится вместе с текстом и озвучивается на вкладке «Озвучка».")+extra))
		ready = append(ready, readyItem{theme.ConfirmIcon(), T("есть озвучка")})
	}
	u.summary.Objects = rows
	u.summary.Refresh()
	u.showReadiness(ready)

	// Подробности
	u.groups = fileGroups(r)
	u.textFiles = r.Of(engine.Text)
	u.audioFiles = audio
	u.statsTable.Refresh()
	u.textList.Refresh()
	u.audioList.Refresh()
	u.archiveInfo.SetText(archiveSummary(r))

	var total int64
	for _, f := range s.Game.RomFS.Files {
		total += f.Size
	}
	u.extractInfo.ParseMarkdown(fmt.Sprintf(T(`**Для перевода извлекать ничего не нужно** — программа читает файлы прямо из образа игры.

Извлечение пригодится, если хочешь:

- послушать музыку и звуки или открыть тексты в других программах (Kuriimu2, Switch Toolbox);
- перевести игру на движке, который программа пока не поддерживает;
- собрать мод вручную.

Файлы попадут в папку `)+"`romfs`"+T(` внутри папки результатов. Все файлы игры займут %s.`), nx.HumanSize(total)))
	u.fillLocales()
	u.fillAudio()
	u.fillVideo()
	u.updateBuildInfo()
	u.setResultsEnabled(true)
}

type readyItem struct {
	icon fyne.Resource
	text string
}

// showReadiness — итог анализа над формой перевода: по строке на текст, шрифты и звук.
func (u *ui) showReadiness(items []readyItem) {
	objs := []fyne.CanvasObject{widget.NewLabelWithStyle(T("Итог анализа"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})}
	for _, it := range items {
		l := widget.NewLabel(it.text)
		l.Wrapping = fyne.TextWrapWord
		objs = append(objs, container.NewBorder(nil, nil, widget.NewIcon(it.icon), nil, l))
	}
	u.readiness.Objects = []fyne.CanvasObject{container.New(layout.NewCustomPaddedVBoxLayout(-theme.Padding()*2), objs...)}
	u.readiness.Refresh()
}

// archiveSummary — что лежит внутри архивов SARC, человеческими словами.
func archiveSummary(r *engine.Report) string {
	inner := r.ArchiveContents()
	archives := 0
	for _, f := range r.Files {
		if strings.HasPrefix(f.Format, "SARC") {
			archives++
		}
	}
	if archives == 0 {
		return T("Для перевода важны только текст, звук и шрифты. Остальное — модели, текстуры, шейдеры и данные уровней.")
	}
	type kv struct {
		ext string
		n   int
	}
	var list []kv
	for e, n := range inner {
		list = append(list, kv{e, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	var parts []string
	for i, x := range list {
		if i == 6 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s — %d", engine.Describe(x.ext), x.n))
	}
	msg := fmt.Sprintf(T("Для перевода важны только текст, звук и шрифты. Большая часть игры — %d архивов SARC"), archives)
	if len(parts) > 0 {
		msg += T(", внутри них: ") + strings.Join(parts, "; ")
	}
	return msg + "."
}

func langName(code string) string {
	for _, l := range translate.Languages {
		if l.Code == code {
			return T(l.Name)
		}
	}
	return code
}

func (u *ui) extract(cats []engine.Category) {
	s := u.session
	u.run(T("Извлечение файлов"), false, func(ctx context.Context) (func(), error) {
		dir, err := s.ExtractFiles(cats, u.progressFn)
		if err != nil {
			return nil, err
		}
		u.logf("Файлы извлечены в %s", dir)
		return nil, nil
	})
}

// ---------- общее ----------

// run выполняет работу в фоне; cancellable — показывать ли кнопку «Остановить».
// Возвращённая функция выполняется в UI-потоке после успеха.
func (u *ui) run(what string, cancellable bool, work func(ctx context.Context) (func(), error)) {
	if u.cancel != nil {
		// одна долгая задача за раз: вторая сбила бы полосу прогресса и кнопку «Остановить» первой
		dialog.ShowInformation(T("Подожди"), T("Сейчас идёт «")+u.busyWhat+T("». Дождись конца или нажми «Остановить»."), u.win)
		return
	}
	u.busyWhat = what
	started := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	u.setBusy(true, cancellable)
	go func() {
		release := keepAwake(what)
		after, err := func() (after func(), err error) {
			defer func() {
				if r := recover(); r != nil {
					after, err = nil, recovered(r)
				}
			}()
			return work(ctx)
		}()
		// fyne.Do выполняется позже, поэтому «остановлено ли пользователем» надо запомнить до cancel()
		stopped := ctx.Err() != nil
		cancel()
		release()
		fyne.Do(func() {
			u.setBusy(false, false)
			u.cancel = nil
			if err != nil {
				if stopped {
					u.addLog(T("Остановлено"))
					return
				}
				u.addLog(T("Ошибка: ") + i18n.Err(err))
				u.notifyAfter(started, what+T(": ошибка — ")+i18n.Err(err))
				u.errorDialog(err)
				return
			}
			u.notifyAfter(started, what+T(": готово"))
			u.status.SetText(T("Готово"))
			u.progress.SetValue(1)
			if after != nil {
				after()
			}
		})
	}()
}

func (u *ui) setBusy(busy, cancellable bool) {
	if busy {
		u.analyzeBtn.Disable()
		u.setResultsEnabled(false)
		u.progress.SetValue(0)
		u.status.SetText(T("Работаю…"))
		u.progress.Hide()
		u.spinner.Show()
		u.spinner.Start()
		u.progressBox.Show()
	} else {
		u.spinner.Stop()
		u.progressBox.Hide()
		u.analyzeBtn.Enable()
		u.setResultsEnabled(u.session != nil)
	}
	if cancellable {
		u.stopBtn.Enable()
	} else {
		u.stopBtn.Disable()
	}
}

func (u *ui) setResultsEnabled(on bool) {
	btns := append(append(append(append(u.resultBtns, u.textButtons()...), u.audioButtons()...), u.videoButtons()...), u.buildButtons()...)
	for _, b := range btns {
		if on {
			b.Enable()
		} else {
			b.Disable()
		}
	}
}

// logf пишет строку в лог; можно вызывать из любой горутины.
func (u *ui) logf(format string, args ...any) {
	u.addLog(fmt.Sprintf(T(format), args...)) // строки лога программы — тоже ключи перевода
}

func (u *ui) addLog(line string) {
	u.logView.add(time.Now().Format("15:04:05  ") + line)
}

// progressFn вызывается из рабочих горутин часто — обновляем экран не чаще 10 раз в секунду.
func (u *ui) progressFn(label string, done, total float64) {
	u.progMu.Lock()
	now := time.Now()
	if done < total && now.Sub(u.progLast) < 100*time.Millisecond {
		u.progMu.Unlock()
		return
	}
	u.progLast = now
	u.progMu.Unlock()
	v := 0.0
	if total > 0 {
		v = done / total
	}
	fyne.Do(func() {
		if !u.progress.Visible() {
			u.spinner.Stop()
			u.spinner.Hide()
			u.progress.Show()
		}
		u.progress.SetValue(v)
		u.status.SetText(fmt.Sprintf("%s: %.0f%%", i18n.Msg(label), v*100))
	})
}

// newSelect задаёт подсказку сразу: у Fyne нет русского перевода для «(Select one)», и он ругается в лог.
func newSelect(options []string, changed func(string)) *widget.Select {
	s := &widget.Select{Options: options, OnChanged: changed, PlaceHolder: T("Выбери…")}
	s.ExtendBaseWidget(s)
	return s
}

func setVisible(o fyne.CanvasObject, v bool) {
	objs := []fyne.CanvasObject{o}
	if l, ok := formLabels[o]; ok {
		objs = append(objs, l)
	}
	for _, x := range objs {
		if v {
			x.Show()
		} else {
			x.Hide()
		}
	}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return 0
}

func labelsOf(opts []translate.Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label()
	}
	return out
}

func optionIndex(opts []translate.Option, id string) int {
	for i, o := range opts {
		if o.ID == id {
			return i
		}
	}
	return -1
}

func speechIndex(opts []speech.Option, id string) int {
	for i, o := range opts {
		if o.ID == id {
			return i
		}
	}
	return -1
}

func speechIndexByLabel(opts []speech.Option, label string) int {
	for i, o := range opts {
		if o.Label() == label {
			return i
		}
	}
	return 0
}

func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ⏎ ") }

var codeRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// displayText — реплика для показа: управляющие коды игры свёрнуты в «‹код›», переносы — в «⏎».
func displayText(s string) string { return oneLine(codeRe.ReplaceAllString(s, T("‹код›"))) }

// formLabels: подпись каждой строки формы — чтобы прятать её вместе с полем.
var formLabels = map[fyne.CanvasObject]fyne.CanvasObject{}

// newForm — форма из пар «подпись, поле». Скрытое через setVisible поле прячет и свою подпись.
func newForm(pairs ...any) *fyne.Container {
	var objs []fyne.CanvasObject
	for i := 0; i+1 < len(pairs); i += 2 {
		label := widget.NewLabelWithStyle(pairs[i].(string), fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
		field := pairs[i+1].(fyne.CanvasObject)
		formLabels[field] = label
		if !field.Visible() {
			label.Hide()
		}
		objs = append(objs, label, field)
	}
	return container.New(layout.NewFormLayout(), objs...)
}

// section — заголовок раздела: спокойный, без карточки.
func section(title string) fyne.CanvasObject {
	return container.NewVBox(widget.NewSeparator(), widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
}

// muted — второстепенный текст.
func muted(text string) *widget.RichText {
	rt := widget.NewRichText(&widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder}})
	rt.Wrapping = fyne.TextWrapWord
	return rt
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return p != "" && err == nil
}
