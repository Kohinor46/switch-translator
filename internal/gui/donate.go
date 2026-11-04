package gui

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Поддержка проекта: перевод картой через CloudTips и USDT в сети TON — как в RenameConvert.

const (
	donateCloudTips = "https://pay.cloudtips.ru/p/253eb33a"
	donateUSDTTON   = "UQBSrz9r4NLiwa13oLu6pmEJVcxldyPba81lkBys5bnhgzJg"

	// после скольких собранных модов один раз напомнить о поддержке
	donateAfterBuilds = 3
	prefBuilds        = "buildsDone"
	prefDonateAsked   = "donateAsked"
)

//go:embed donate_qr.png
var donateQR []byte

//go:embed icon.png
var appIcon []byte

// showDonate — окно «Поддержать проект».
func (u *ui) showDonate() {
	intro := widget.NewLabel(T("Switch Translator бесплатный. Если он тебе пригодился — поддержи разработку."))
	intro.Wrapping = fyne.TextWrapWord

	card := widget.NewButtonWithIcon(T("Перевести через CloudTips"), theme.ComputerIcon(), func() { u.openURL(donateCloudTips) })
	card.Importance = widget.HighImportance

	qr := canvas.NewImageFromResource(fyne.NewStaticResource("donate_qr.png", donateQR))
	qr.FillMode = canvas.ImageFillContain
	qr.ScaleMode = canvas.ImageScalePixels
	qr.SetMinSize(fyne.NewSize(150, 150))

	addr := widget.NewEntry()
	addr.SetText(donateUSDTTON)
	addr.OnChanged = func(string) { addr.SetText(donateUSDTTON) } // только для чтения, но выделять можно
	var copyBtn *widget.Button
	copyBtn = widget.NewButtonWithIcon(T("Скопировать адрес"), theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(donateUSDTTON)
		copyBtn.SetText(T("Скопировано"))
		copyBtn.SetIcon(theme.ConfirmIcon())
	})
	warn := widget.NewLabel(T("Отправляй только USDT в сети TON — иначе средства могут потеряться."))
	warn.Wrapping = fyne.TextWrapWord
	warn.Importance = widget.WarningImportance

	heading := func(s string) *widget.Label {
		return widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	crypto := container.NewBorder(nil, nil, container.NewPadded(qr), nil,
		container.NewVBox(heading("USDT · TON"), addr, container.NewHBox(copyBtn), warn))
	content := container.NewVBox(
		intro,
		widget.NewSeparator(),
		heading(T("Банковской картой")),
		container.NewBorder(nil, nil, widget.NewLabel(T("Карты российских банков")), nil, container.NewHBox(card)),
		widget.NewSeparator(),
		heading(T("Криптовалютой")),
		crypto,
	)
	d := dialog.NewCustom(T("Поддержать проект"), T("Закрыть"), content, u.win)
	d.Resize(fyne.NewSize(620, 0))
	d.Show()
}

// countBuild — мод собран: после нескольких сборок один раз предложить поддержать проект.
func (u *ui) countBuild() {
	n := u.prefs.Int(prefBuilds) + 1
	u.prefs.SetInt(prefBuilds, n)
	if n >= donateAfterBuilds && !u.prefs.Bool(prefDonateAsked) {
		u.prefs.SetBool(prefDonateAsked, true)
		u.showDonate()
	}
}
