package gui

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/nx"
	"switch-translator/internal/project"
)

// buildTab — вкладка «Сборка»: мод для Atmosphere из перевода и озвучки.
type buildTab struct {
	buildTabItem *container.TabItem
	buildInfo    *widget.RichText // что войдёт в мод
	buildAudio   *widget.Check
	fitDubs      *widget.Check
	glyphs       *widget.Check // дорисовать буквы в шрифты игры
	ttfPath      string        // свой TTF/OTF для новых букв
	ttfLabel     *widget.Label
	ttfRow       *fyne.Container
	ttfClear     *widget.Button
	fontPath     string
	fontLabel    *widget.Label
	fontClear    *widget.Button
	buildBtn     *widget.Button
	imageBtn     *widget.Button
	imageFormat  *widget.RadioGroup
	buildResult  *fyne.Container
}

func (u *ui) buildButtons() []*widget.Button {
	if u.buildBtn == nil {
		return nil
	}
	return []*widget.Button{u.buildBtn, u.imageBtn}
}

func (u *ui) buildBuildTab() fyne.CanvasObject {
	intro := widget.NewRichTextFromMarkdown(T("Перевод можно собрать двумя способами.\n\n") +
		T("**Мод** — папка `atmosphere` только с изменёнными файлами игры. Её копируют на карту памяти консоли с Atmosphere, ") +
		T("и игра подхватывает перевод; чтобы вернуть оригинал, папку удаляют. Так устроены готовые русификаторы.\n\n") +
		T("**Образ NSP/NSZ** — игра со всеми обновлениями, DLC и переводом одним файлом для установщика (DBI, Tinfoil, Awoo). ") +
		T("Игра внутри не меняется, перевод ставится как её обновление."))
	intro.Wrapping = fyne.TextWrapWord

	u.buildInfo = widget.NewRichText()
	u.buildInfo.Wrapping = fyne.TextWrapWord
	u.buildAudio = widget.NewCheck(T("Вписать озвучку"), func(on bool) { setVisible(u.fitDubs, on && u.buildAudio.Visible()) })
	u.fitDubs = widget.NewCheck(strings.Replace(fmt.Sprintf(T("Подогнать по длине под оригинал (ускорить длинные фразы до %.1f раза)"), project.MaxSpeed), ".", ",", 1),
		func(on bool) { u.prefs.SetBool("fitDubs", on) })
	u.fitDubs.Checked = u.prefs.BoolWithFallback("fitDubs", true)

	u.glyphs = widget.NewCheck(T("Дорисовать русские буквы в шрифты игры"), func(bool) { u.updateBuildInfo() })
	u.glyphs.Checked = true
	glyphHelp := widget.NewRichTextFromMarkdown(T("В шрифтах большинства игр нет русских букв, и вместо них были бы пустые места. ") +
		T("Программа дорисует недостающие буквы в стиле игры: подгонит размер, толщину и обводку под латиницу шрифта, ") +
		T("а буквы, похожие на латинские (А, В, Е, К, о, р, с…), возьмёт из самой игры. После сборки покажет, как получилось."))
	glyphHelp.Wrapping = fyne.TextWrapWord
	u.ttfLabel = widget.NewLabel("")
	u.ttfLabel.Wrapping = fyne.TextWrapWord
	var pickTTF *widget.Button
	pickTTF = widget.NewButtonWithIcon(T("Другой шрифт…"), theme.FileIcon(), func() {
		u.pick(pickRequest{
			kind:     pickFile,
			title:    T("Шрифт с кириллицей (TTF или OTF)"),
			current:  u.ttfPath,
			patterns: []string{"*.ttf", "*.otf"},
			btn:      pickTTF,
			done: func(p string) {
				u.ttfPath = p
				u.updateBuildInfo()
			},
		})
	})
	u.ttfClear = widget.NewButtonWithIcon(T("Встроенный"), theme.ContentClearIcon(), func() {
		u.ttfPath = ""
		u.updateBuildInfo()
	})
	u.ttfClear.Importance = widget.LowImportance
	u.ttfRow = container.NewVBox(u.ttfLabel, container.NewHBox(pickTTF, u.ttfClear))

	u.fontLabel = widget.NewLabel("")
	u.fontLabel.Wrapping = fyne.TextWrapWord
	var pickFont *widget.Button
	pickFont = widget.NewButtonWithIcon(T("Выбрать шрифт…"), theme.FileIcon(), func() {
		u.pick(pickRequest{
			kind:     pickFile,
			title:    T("Шрифт из русификатора (FontData.szs или .bffnt)"),
			current:  u.fontPath,
			patterns: []string{"*.szs", "*.bffnt", "*.sarc", "*.zs"},
			btn:      pickFont,
			done: func(p string) {
				u.fontPath = p
				u.updateBuildInfo()
			},
		})
	})
	u.fontClear = widget.NewButtonWithIcon(T("Убрать"), theme.ContentClearIcon(), func() {
		u.fontPath = ""
		u.updateBuildInfo()
	})
	u.fontClear.Importance = widget.LowImportance
	fontHelp := widget.NewRichTextFromMarkdown(T("Если у игры уже есть русификатор, можно взять шрифт из него ") +
		T("(обычно `FontData.szs` или файлы `.bffnt`): он должен называться так же, как файл игры, который заменяет. ") +
		T("Тогда программа ничего не дорисовывает."))
	fontHelp.Wrapping = fyne.TextWrapWord
	ready := widget.NewAccordion(widget.NewAccordionItem(T("Готовый шрифт из русификатора"),
		container.NewVBox(fontHelp, u.fontLabel, container.NewHBox(pickFont, u.fontClear))))

	u.buildBtn = widget.NewButtonWithIcon(T("Собрать мод"), theme.DownloadIcon(), u.buildMod)
	u.buildBtn.Importance = widget.HighImportance
	u.buildBtn.Disable()
	u.imageFormat = widget.NewRadioGroup([]string{"NSZ", "NSP"}, nil)
	u.imageFormat.Horizontal = true
	u.imageFormat.Selected = "NSZ"
	u.imageBtn = widget.NewButtonWithIcon(T("Собрать образ"), theme.StorageIcon(), u.buildImage)
	u.imageBtn.Disable()
	u.buildResult = container.NewVBox()

	why := widget.NewRichTextFromMarkdown(T("**Мод** весит несколько мегабайт, ставится копированием и не мешает ") +
		T("официальным обновлениям — но после обновления игры мод нужно собрать заново под новую версию.\n\n") +
		T("**Образ** удобнее раздавать: один файл, внутри игра, последнее обновление, DLC и перевод. Перевод в нём — ") +
		T("это обновление игры с версией выше официальной, поэтому консоль не заменит его тем же обновлением. ") +
		T("Минусы: весит как игра, а устанавливается только на консоль с патчами подписи (sigpatches) — ") +
		T("подписать изменённые файлы подписью Nintendo невозможно. NSZ сжат и примерно вдвое меньше NSP, ") +
		T("но его понимают не все установщики; NSP — все."))
	why.Wrapping = fyne.TextWrapWord

	return container.NewVScroll(container.NewPadded(container.NewVBox(
		intro,
		section(T("Что войдёт в мод")), u.buildInfo, u.buildAudio, u.fitDubs,
		section(T("Шрифт")), u.glyphs, glyphHelp, u.ttfRow, ready,
		widget.NewSeparator(),
		container.NewHBox(u.buildBtn, widget.NewSeparator(), u.imageBtn, u.imageFormat),
		u.buildResult,
		widget.NewAccordion(widget.NewAccordionItem(T("Мод или образ?"), why)),
	)))
}

// updateBuildInfo пересчитывает, что войдёт в мод: перевод выбранного языка и готовая озвучка.
func (u *ui) updateBuildInfo() {
	if u.buildInfo == nil {
		return
	}
	if u.fontPath == "" {
		u.fontLabel.SetText(T("Не выбран."))
		u.fontClear.Hide()
		u.glyphs.Enable()
	} else {
		u.fontLabel.SetText(T("Заменить шрифт игры на: ") + filepath.Base(u.fontPath))
		u.fontClear.Show()
		u.glyphs.Disable()
	}
	if u.ttfPath == "" {
		u.ttfLabel.SetText(T("Буквы рисуются из встроенного Noto Sans Bold."))
		u.ttfClear.Hide()
	} else {
		u.ttfLabel.SetText(T("Буквы рисуются из ") + filepath.Base(u.ttfPath) + ".")
		u.ttfClear.Show()
	}
	setVisible(u.ttfRow, u.fontPath == "" && u.glyphs.Checked)
	if u.session == nil {
		u.buildInfo.ParseMarkdown(T("Сначала открой игру на вкладке «Игра»."))
		u.buildAudio.Hide()
		u.fitDubs.Hide()
		return
	}
	lines := 0
	for _, s := range u.session.Strings {
		if s.Translation != "" && !s.Skip {
			lines++
		}
	}
	dubs := 0
	for _, l := range u.session.Audio {
		if l.Selected && l.Dub != "" {
			dubs++
		}
	}
	text := fmt.Sprintf(T("**Текст:** переведено %d из %d реплик"), lines, len(u.session.Strings))
	if u.session.Locale != "" {
		text += fmt.Sprintf(T(". Перевод заменит язык %s — его и нужно выбрать в настройках консоли."), project.ConsoleLanguage(u.session.Locale))
	}
	recognized, translated := 0, 0
	for _, l := range u.session.Audio {
		if l.Selected && l.Recognized && strings.TrimSpace(l.Transcript) != "" {
			recognized++
			if l.Translation != "" {
				translated++
			}
		}
	}
	switch {
	case dubs > 0:
		text += fmt.Sprintf(T("\n\n**Звук:** озвучено %d реплик — чтобы вписать их, оставь галочку ниже."), dubs)
	case recognized > 0:
		text += fmt.Sprintf(T("\n\n**Звук:** речь распознана в %d файлах, переведено %d, озвучено 0 — голоса в сборке останутся оригинальными. ")+
			T("Чтобы вписать русскую речь: переведи её на вкладке «Перевод», ")+
			T("потом озвучь на вкладке «Озвучка»."), recognized, translated)
	}
	videos := 0
	for _, l := range u.session.Video {
		if l.Selected && l.Output != "" {
			videos++
		}
	}
	if videos > 0 {
		text += fmt.Sprintf(T("\n\n**Видео:** переведённых роликов — %d."), videos)
	}
	if lines == 0 && videos == 0 {
		text += T("\n\nПереведи текст на вкладке «Перевод» — без перевода собирать нечего.")
	}
	u.buildInfo.ParseMarkdown(text)
	u.buildAudio.SetText(fmt.Sprintf(T("Вписать озвучку (%d файлов готово)"), dubs))
	setVisible(u.buildAudio, dubs > 0)
	if dubs == 0 {
		u.buildAudio.SetChecked(false)
	}
	setVisible(u.fitDubs, dubs > 0 && u.buildAudio.Checked)
}

func (u *ui) buildMod() {
	if u.session == nil {
		return
	}
	opts := u.buildOptions()
	var res *project.BuildResult
	u.run(T("Сборка мода"), false, func(ctx context.Context) (func(), error) {
		var err error
		res, err = u.session.Build(opts, u.progressFn)
		if err != nil {
			return nil, err
		}
		u.logf("Мод собран: %s (%d реплик, %d озвучек)", res.Dir, res.Lines, res.Dubs)
		return func() { u.showBuildResult(res, nil) }, nil
	})
}

func (u *ui) buildOptions() project.BuildOptions {
	return project.BuildOptions{To: u.toLang(), Audio: u.buildAudio.Checked, FitDubs: u.fitDubs.Checked, Video: true, FontFile: u.fontPath,
		Glyphs: u.glyphs.Checked, GlyphTTF: u.ttfPath}
}

// buildImage собирает NSP/NSZ: игра + своё обновление с переводом + DLC, и проверяет его.
func (u *ui) buildImage() {
	if u.session == nil {
		return
	}
	opts := project.ImageOptions{BuildOptions: u.buildOptions(), NSZ: u.imageFormat.Selected != "NSP"}
	u.run(T("Сборка образа"), false, func(ctx context.Context) (func(), error) {
		img, err := u.session.BuildImage(opts, u.progressFn)
		if err != nil {
			return nil, err
		}
		if err := u.session.VerifyImage(img, u.progressFn); err != nil {
			return nil, fmt.Errorf(T("образ собран, но не прошёл проверку: %w"), err)
		}
		u.logf("Образ собран и проверен: %s", img.Path)
		return func() { u.showBuildResult(img.Mod, img) }, nil
	})
}

func (u *ui) showBuildResult(res *project.BuildResult, img *project.ImageResult) {
	var objs []fyne.CanvasObject
	st := widget.NewRichText()
	st.Wrapping = fyne.TextWrapWord
	if img != nil {
		richStatus(st, true, T("Образ собран и проверен: ")+filepath.Base(img.Path),
			fmt.Sprintf(T("%s. Внутри: %s. Перевод: %d реплик, %d озвучек."), nx.HumanSize(img.Size), strings.Join(img.Contents, "; "), res.Lines, res.Dubs))
	} else {
		richStatus(st, true, T("Мод собран"), fmt.Sprintf(T("%d реплик, %d озвучек, %d роликов, файлов: %d"), res.Lines, res.Dubs, res.Videos, len(res.Files)))
	}
	objs = append(objs, st)
	if res.Sped > 0 || res.TooLong > 0 {
		w := widget.NewRichText()
		w.Wrapping = fyne.TextWrapWord
		detail := fmt.Sprintf(T("Ускорено %d из %d озвучек, чтобы уложиться в длину оригинала."), res.Sped, res.Dubs)
		if res.TooLong == 0 {
			richStatus(w, true, T("Озвучка подогнана по длине"), detail)
		} else {
			richStatus(w, false, fmt.Sprintf(T("Озвучка: %d реплик всё равно длиннее оригинала"), res.TooLong),
				detail+T(" Остальные не влезли даже ускоренными — их список в логе. Сократи им перевод в правке звука ")+
					T("(щелчок по строке на вкладке «Озвучка»), озвучь заново и собери ещё раз; иначе игра может их оборвать."))
		}
		objs = append(objs, w)
	}
	if len(res.Fonts) > 0 {
		names := make([]string, len(res.Fonts))
		for i, f := range res.Fonts {
			names[i] = path.Base(f)
		}
		info := widget.NewRichText()
		info.Wrapping = fyne.TextWrapWord
		richStatus(info, true, fmt.Sprintf(T("Дорисовано букв: %d"), len(res.Drawn)), T("В шрифтах: ")+strings.Join(names, ", ")+T(". Так перевод будет выглядеть в игре:"))
		objs = append(objs, info)
		if res.Preview != "" {
			img := canvas.NewImageFromFile(res.Preview)
			img.FillMode = canvas.ImageFillContain
			img.SetMinSize(fyne.NewSize(560, 160))
			objs = append(objs, img)
		}
	}
	if len(res.Missing) > 0 {
		w := widget.NewRichText()
		w.Wrapping = fyne.TextWrapWord
		chars := string(res.Missing)
		if len([]rune(chars)) > 60 {
			chars = string([]rune(chars)[:60]) + "…"
		}
		hint := T("В игре на их месте будут пустые места. Включи дорисовку букв, выбери другой TTF или готовый шрифт и собери мод заново.")
		if u.glyphs.Checked {
			hint = T("В игре на их месте будут пустые места. Дорисовать их не вышло: дорисовка работает только в растровых шрифтах игры. Попробуй готовый шрифт из русификатора.")
		}
		richStatus(w, false, T("В шрифтах игры нет этих символов: ")+chars, hint)
		objs = append(objs, w)
	} else if res.FontsOK && len(res.Fonts) == 0 {
		w := widget.NewRichText()
		w.Wrapping = fyne.TextWrapWord
		richStatus(w, true, T("Шрифты в порядке"), T("Все буквы перевода уже есть в шрифтах игры — дорисовывать ничего не пришлось."))
		objs = append(objs, w)
	}
	for _, warn := range res.Warnings {
		w := widget.NewRichText()
		w.Wrapping = fyne.TextWrapWord
		richStatus(w, false, warn, "")
		objs = append(objs, w)
	}
	how := widget.NewRichTextFromMarkdown(fmt.Sprintf(T("**Как установить:** скопируй папку `atmosphere` из папки «мод» в корень карты памяти ")+
		T("и выбери на консоли язык %s. Подробности — в файле «ПРОЧТИ.txt» рядом."), project.ConsoleLanguage(u.session.Locale)))
	dir := res.Dir
	if img != nil {
		how = widget.NewRichTextFromMarkdown(fmt.Sprintf(T("**Как установить:** установи файл через DBI, Tinfoil или Awoo — ")+
			T("на консоли нужны патчи подписи (sigpatches). Если игра или её обновление уже стоят, установщик их дополнит. ")+
			T("В настройках консоли выбери язык %s. В «Управлении ПО» у игры будет версия с пометкой «-%s»."),
			T(project.ConsoleLanguage(u.session.Locale)), strings.ToUpper(u.toLang())))
		dir = filepath.Dir(img.Path)
	}
	how.Wrapping = fyne.TextWrapWord
	open := widget.NewButtonWithIcon(revealLabel(), theme.FolderOpenIcon(), func() {
		u.app.OpenURL(&url.URL{Scheme: "file", Path: dir})
	})
	objs = append(objs, how, container.NewHBox(open))
	u.buildResult.Objects = objs
	u.buildResult.Refresh()
	u.countBuild()
}
