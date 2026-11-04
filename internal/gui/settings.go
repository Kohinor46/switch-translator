package gui

import (
	"net/url"
	"time"

	"fyne.io/fyne/v2/lang"

	"switch-translator/internal/i18n"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// appearance — цветовая схема из настроек.
type appearance int

const (
	appearanceSystem appearance = iota // как в системе
	appearanceLight
	appearanceDark

	numAppearances

	prefAppearance = "appearance"
	prefNotify     = "notifications"
	prefLanguage   = "language"

	// notifyMin: о коротких операциях не уведомляем — пользователь ещё смотрит на окно.
	// (macOS и сама не показывает баннеры приложения, которое на переднем плане.)
	notifyMin = 10 * time.Second
)

func (a appearance) name() string {
	return [...]string{T("Как в системе"), T("Светлая"), T("Тёмная")}[a]
}

func loadAppearance(p fyne.Preferences) appearance {
	a := appearance(p.IntWithFallback(prefAppearance, int(appearanceSystem)))
	if a < 0 || a >= numAppearances {
		return appearanceSystem
	}
	return a
}

func (a appearance) theme() fyne.Theme {
	switch a {
	case appearanceLight:
		return stTheme{variant: theme.VariantLight, fixed: true}
	case appearanceDark:
		return stTheme{variant: theme.VariantDark, fixed: true}
	}
	return stTheme{}
}

func (u *ui) setAppearance(a appearance) {
	u.appearance = a
	u.prefs.SetInt(prefAppearance, int(a))
	u.app.Settings().SetTheme(a.theme())
}

// uiLanguage — язык интерфейса: выбранный в настройках, иначе язык системы (если перевода нет — английский).
func uiLanguage(p fyne.Preferences) string {
	if c := p.String(prefLanguage); c != "" {
		return c
	}
	return i18n.Match(lang.SystemLocale().String())
}

// showAbout — версия, ссылка на страницу проекта.
func (u *ui) showAbout() {
	link, _ := url.Parse("https://github.com/" + repository)
	content := container.NewVBox(
		widget.NewLabelWithStyle("Switch Translator "+Version, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel(T("Перевод игр Nintendo Switch: текст, озвучка, ролики и шрифты.")),
		widget.NewHyperlink("github.com/"+repository, link),
	)
	dialog.ShowCustom(T("О программе"), T("Закрыть"), content, u.win)
}

// showSettings — окно настроек: оформление и уведомления. Изменения применяются сразу и запоминаются.
func (u *ui) showSettings() {
	looks := make([]string, numAppearances)
	for a := range numAppearances {
		looks[a] = a.name()
	}
	look := widget.NewRadioGroup(looks, nil)
	look.Horizontal = true
	look.Required = true
	look.SetSelected(u.appearance.name())
	look.OnChanged = func(name string) {
		for a := range numAppearances {
			if a.name() == name && a != u.appearance {
				u.setAppearance(a)
			}
		}
	}

	heading := func(text string) *widget.Label {
		return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	notify := widget.NewCheck(T("Уведомлять, когда долгая операция закончилась"), func(on bool) { u.prefs.SetBool(prefNotify, on) })
	notify.SetChecked(u.prefs.BoolWithFallback(prefNotify, true))
	testNotify := widget.NewButton(T("Проверить"), func() {
		u.app.SendNotification(fyne.NewNotification("Switch Translator", T("Так будут выглядеть уведомления.")))
	})

	// язык: окно собирается заново при запуске, поэтому новый язык включается после перезапуска
	langNames := make([]string, len(i18n.Languages))
	for i, l := range i18n.Languages {
		langNames[i] = l.Name
	}
	langSel := widget.NewSelect(langNames, nil)
	for _, l := range i18n.Languages {
		if l.Code == i18n.Current() {
			langSel.SetSelected(l.Name)
		}
	}
	langSel.OnChanged = func(name string) {
		for _, l := range i18n.Languages {
			if l.Name == name && l.Code != i18n.Current() {
				u.prefs.SetString(prefLanguage, l.Code)
				dialog.ShowCustomConfirm(T("Язык интерфейса"), T("Перезапустить"), T("Позже"),
					widget.NewLabel(T("Язык сменится после перезапуска программы.")), func(ok bool) {
						if ok {
							relaunch(u.app)
						}
					}, u.win)
			}
		}
	}
	updates := widget.NewCheck(T("Проверять обновления при запуске"), func(on bool) { u.prefs.SetBool(prefCheckUpdates, on) })
	updates.SetChecked(u.prefs.BoolWithFallback(prefCheckUpdates, true))

	content := container.NewVBox(
		heading(T("Общие")),
		container.NewBorder(nil, nil, widget.NewLabel(T("Язык")), nil, langSel),
		container.NewBorder(nil, nil, nil, testNotify, notify),
		updates,
		widget.NewSeparator(),
		heading(T("Оформление")),
		look,
	)
	d := dialog.NewCustom(T("Настройки"), T("Закрыть"), content, u.win)
	d.Resize(fyne.NewSize(560, 340))
	d.Show()
}

// notifyAfter показывает системное уведомление об операции, начатой в start,
// если она шла достаточно долго и уведомления включены.
func (u *ui) notifyAfter(start time.Time, text string) {
	if time.Since(start) < notifyMin || !u.prefs.BoolWithFallback(prefNotify, true) {
		return
	}
	u.app.SendNotification(fyne.NewNotification("Switch Translator", text))
}
