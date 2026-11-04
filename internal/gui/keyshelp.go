package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// keysHelpShort — короткая подсказка про ключи на языке интерфейса.
func keysHelpShort() string {
	return T(`**Где взять ключи.** Образы игр зашифрованы, и расшифровать их можно только ключами **твоей** консоли: файлы **prod.keys** и **title.keys**. Их сохраняет на карту памяти программа Lockpick_RCM.

**Если приставка уже прошита**, ключи, скорее всего, уже лежат на карте памяти в папке **switch** (`) + "`switch/prod.keys`, `switch/title.keys`" + T(`). Вставь карту в компьютер и нажми **«Найти»** — программа сама заглянет на карту и в ~/.switch.`)
}

const keysHelpFull = `### Что это за файлы

- **prod.keys** — ключи консоли. Без них образ не расшифровать.
- **title.keys** — ключи купленных игр. Нужен, если в образе NSP/NSZ нет тикета (программа скажет об этом сама).

Брать ключи нужно **со своей** приставки: скачивать чужие из интернета незаконно.

### Где их взять

1. На прошитой приставке запусти payload **Lockpick_RCM** (через Hekate: *Payloads → Lockpick_RCM.bin*) и выбери *Dump from SysNAND*.
2. Lockpick сохранит ключи на карту памяти: **switch/prod.keys** и **switch/title.keys**.
3. Если приставка уже прошита и Lockpick запускали раньше — файлы уже там.

### Как отдать их программе

- Вставь карту памяти в компьютер и нажми **«Найти»**, или
- скопируй оба файла в папку **~/.switch/** (на Windows — **C:\Users\<имя>\.switch\**): там их ищут и эта программа, и другие утилиты для Switch, или
- выбери файл кнопкой **«Выбрать…»** где угодно.

### Если программа пишет «ключи старее игры»

Игра вышла под более новую прошивку, чем ключи. Обнови прошивку приставки и запусти Lockpick_RCM ещё раз — он сохранит новые ключи.`

func (u *ui) showKeysHelp() {
	text := widget.NewRichTextFromMarkdown(T(keysHelpFull))
	text.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustom(T("Ключи: где взять"), T("Понятно"), container.NewVScroll(text), u.win)
	d.Resize(fyne.NewSize(680, 560))
	d.Show()
}
