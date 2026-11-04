package gui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"switch-translator/internal/i18n"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"switch-translator/internal/gametext"
	"switch-translator/internal/translate"
)

// textTab — вкладка «Перевод»: выбор переводчика, словарь, выбор строк, перевод всего текста и речи, ручная правка.
type textTab struct {
	fromSel, toSel *widget.Select
	localeSel      *widget.Select
	fromInfo       *widget.Label // язык оригинала на «Переводе»: выбирается на «Что нашлось»
	localeCodes    []string
	readiness      *fyne.Container // «что можно перевести» — сжатый итог со вкладки 2

	textProv     *widget.Select
	textKey      *widget.Entry
	textURL      *widget.Entry
	textModel    *widget.SelectEntry
	textNote     *widget.RichText
	textHelp     *fyne.Container
	cliStatus    *widget.RichText
	gameContext  *widget.Entry
	contextBox   fyne.CanvasObject
	textLeft     *fyne.Container // левая колонка: после смены подписей её надо переложить
	estimate     *widget.Label
	translateBtn *widget.Button

	stringsList   *widget.List
	view          []int // какие строки сейчас видны в списке (индексы session.Strings)
	search        *widget.Entry
	counter       *widget.Label
	selAll        *widget.Button
	selNone       *widget.Button
	editor        fyne.CanvasObject
	stringsPane   *fyne.Container
	edIdx         int
	edSource      *widget.Label
	edEntry       *widget.Entry
	edSave, edOne *widget.Button
}

func (u *ui) textButtons() []*widget.Button {
	return append([]*widget.Button{u.translateBtn, u.selAll, u.selNone, u.edSave, u.edOne}, u.glossaryBtns...)
}

func langOptions() []string {
	out := make([]string, len(translate.Languages))
	for i, l := range translate.Languages {
		out[i] = l.Code + " — " + T(l.Name)
	}
	return out
}

func langCode(sel string) string { c, _, _ := strings.Cut(sel, " "); return c }

func selectByCode(s *widget.Select, code string) {
	for _, o := range s.Options {
		if langCode(o) == code {
			s.SetSelected(o)
			return
		}
	}
}

func vendor(id string) string { return strings.TrimSuffix(strings.TrimSuffix(id, "-stt"), "-tts") }

func (u *ui) fromLang() string {
	if u.session != nil && u.session.Locale != "" {
		return gametext.LocaleLang(u.session.Locale)
	}
	return langCode(u.fromSel.Selected)
}

func (u *ui) toLang() string { return langCode(u.toSel.Selected) }

func (u *ui) buildTextTab() fyne.CanvasObject {
	u.fromInfo = widget.NewLabel("")
	u.fromInfo.Wrapping = fyne.TextWrapWord
	u.toSel = newSelect(langOptions(), func(string) {
		u.prefs.SetString("to", u.toLang())
		u.reloadStrings()
	})
	selectByCode(u.toSel, u.prefs.StringWithFallback("to", "ru"))
	u.updateFromInfo()

	opts := translate.Options()
	labels := labelsOf(opts)
	u.textKey = widget.NewPasswordEntry()
	u.textURL = widget.NewEntry()
	u.textModel = widget.NewSelectEntry(nil)
	u.textModel.OnChanged = func(string) { u.updateEstimate() }
	u.textNote = muted("")
	u.textHelp = container.NewVBox()
	u.cliStatus = widget.NewRichText()
	u.cliStatus.Wrapping = fyne.TextWrapBreak
	u.gameContext = widget.NewMultiLineEntry()
	u.gameContext.Wrapping = fyne.TextWrapWord
	u.gameContext.SetPlaceHolder(T("Например: «Captain Toad. Toad = Тоад, Toadette = Тоадетта. Короткие подсказки интерфейса — в повелительном наклонении.»"))
	u.gameContext.SetText(u.prefs.String("context"))
	u.gameContext.SetMinRowsVisible(3)
	u.contextBox = container.NewVBox(
		widget.NewLabelWithStyle(T("Контекст для ИИ"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		muted(T("Что за игра, как зовут героев, каким тоном говорить — ИИ учтёт это во всех строках.")),
		u.gameContext,
	)
	u.estimate = widget.NewLabel("")
	u.estimate.Wrapping = fyne.TextWrapWord

	u.textProv = newSelect(labels, func(sel string) { u.onProvider(opts[indexOf(labels, sel)]) })
	u.translateBtn = widget.NewButton(T("3. Перевести отмеченное"), u.translateText)
	u.translateBtn.Importance = widget.HighImportance

	form := newForm(
		T("Переводить с"), u.fromInfo,
		T("На язык"), u.toSel,
		T("Переводчик"), u.textProv,
		T("Ключ"), u.textKey,
		T("Адрес сервера"), u.textURL,
		T("Модель"), u.textModel,
	)
	next := widget.NewButtonWithIcon(T("Дальше — озвучка"), theme.NavigateNextIcon(), func() { u.tabs.Select(u.dubTab) })
	next.Importance = widget.LowImportance
	u.readiness = container.NewVBox()
	u.textLeft = container.NewVBox(
		u.readiness,
		form,
		u.textNote,
		u.cliStatus,
		u.textHelp,
		u.contextBox,
		widget.NewSeparator(),
		u.buildGlossaryBox(),
		widget.NewSeparator(),
		section(T("Перевод")),
		muted(T("Переводятся вместе, по одному словарю: отмеченные реплики справа, речь из звука и фразы роликов, распознанные на вкладке «Речь в текст». ")+
			T("Одинаковые реплики и речь уходят переводчику один раз.")),
		u.estimate,
		container.NewHBox(u.translateBtn),
		widget.NewSeparator(),
		container.NewHBox(next),
	)
	left := container.NewVScroll(u.textLeft)
	u.textProv.SetSelectedIndex(max(0, optionIndex(opts, u.prefs.String("textProvider"))))

	return container.NewHSplit(left, u.buildStringsPane())
}

// videoToTranslate — отмеченные ролики, где есть непереведённые фразы, и сами фразы.
func (u *ui) videoToTranslate() (idx []int, texts []string) {
	for i, v := range u.session.Video {
		if !v.Selected {
			continue
		}
		n := len(texts)
		for _, sg := range v.Segments {
			if sg.Text != "" && sg.Translation == "" {
				texts = append(texts, sg.Text)
			}
		}
		if len(texts) > n {
			idx = append(idx, i)
		}
	}
	return idx, texts
}

// onProvider перестраивает поля под выбранный переводчик.
func (u *ui) onProvider(o translate.Option) {
	u.prefs.SetString("textProvider", o.ID)
	key := u.prefs.String("apikey." + vendor(o.ID))
	u.textKey.SetText(key)
	setVisible(u.textKey, o.KeyLabel != "")
	if l, ok := formLabels[u.textKey].(*widget.Label); ok {
		label := T(o.KeyLabel)
		if !o.NeedsKey {
			label += T(" (необязательно)")
		}
		l.SetText(label)
	}
	switch o.ID {
	case "mymemory":
		u.textKey.Password = false
		u.textKey.SetPlaceHolder(T("твой e-mail — тогда лимит 50 000 символов в день"))
	case "libre":
		u.textKey.Password = true
		u.textKey.SetPlaceHolder(T("на своём сервере не нужен"))
	default:
		u.textKey.Password = true
		u.textKey.SetPlaceHolder(T("вставь ключ сюда"))
	}
	u.textKey.Refresh()

	u.textURL.SetText(u.prefs.StringWithFallback("url."+o.ID, o.URL))
	setVisible(u.textURL, o.NeedsURL)
	var models []string
	for _, m := range o.Models {
		if m != "" {
			models = append(models, m)
		}
	}
	u.textModel.SetOptions(models)
	model := u.prefs.String("model." + o.ID)
	if model == "" && len(o.Models) > 0 {
		model = o.Models[0]
	}
	u.textModel.SetText(model)
	u.textModel.SetPlaceHolder(T("по умолчанию"))
	setVisible(u.textModel, len(o.Models) > 0)
	setVisible(u.contextBox, o.LLM)
	setMuted(u.textNote, T(o.Note))

	// Для Codex и Claude Code — нашлась ли программа.
	switch o.ID {
	case "codex", "claudecode":
		find := translate.FindCodex
		if o.ID == "claudecode" {
			find = translate.FindClaudeCode
		}
		if p, err := find(); err == nil {
			richStatus(u.cliStatus, true, T("Найден"), p)
		} else {
			richStatus(u.cliStatus, false, i18n.Err(err), "")
		}
		u.cliStatus.Show()
	default:
		u.cliStatus.Hide()
	}
	open := (o.NeedsKey && key == "") || o.Tier == translate.Subscription || o.ID == "libre"
	setHelp(u.textHelp, T("Как подключить «")+T(o.Title)+"»", u.helpContent(o.Site, o.Help, o.Commands), open)
	u.updateEstimate()
	u.updateGlossaryInfo()
}

// ---------- список строк и правка ----------

func (u *ui) buildStringsPane() fyne.CanvasObject {
	u.search = widget.NewEntry()
	u.search.SetPlaceHolder(T("Поиск по репликам"))
	u.search.OnChanged = func(string) { u.refreshStrings() }
	u.counter = widget.NewLabel("")
	u.selAll = widget.NewButton(T("Отметить все"), func() { u.markView(true) })
	u.selNone = widget.NewButton(T("Снять все"), func() { u.markView(false) })
	for _, b := range []*widget.Button{u.selAll, u.selNone} {
		b.Importance = widget.LowImportance
	}

	u.stringsList = widget.NewList(
		func() int { return len(u.view) },
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			// оригинал приглушён, перевод — обычным цветом со стрелкой: различаются не только цветом
			src := &widget.TextSegment{Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder, Inline: true}}
			dst := &widget.TextSegment{Style: widget.RichTextStyleInline}
			rt := widget.NewRichText(src, &widget.TextSegment{Text: "\n", Style: widget.RichTextStyleInline}, dst)
			rt.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, chk, nil, rt)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			i := u.view[id]
			str := u.session.Strings[i]
			c := o.(*fyne.Container)
			rt, chk := c.Objects[0].(*widget.RichText), c.Objects[1].(*widget.Check)
			chk.OnChanged = nil
			chk.SetChecked(!str.Skip)
			chk.OnChanged = func(on bool) {
				u.session.Strings[i].Skip = !on
				u.saveStrings()
				u.updateCounter()
			}
			rt.Segments[0].(*widget.TextSegment).Text = displayText(str.Source)
			dst := rt.Segments[2].(*widget.TextSegment)
			switch {
			case str.Translation != "":
				dst.Text = "→ " + displayText(str.Translation)
				dst.Style.ColorName = ""
			case str.Skip && !gametext.HasLetters(str.Source):
				dst.Text = T("→ переводить нечего: только служебные коды")
				dst.Style.ColorName = theme.ColorNamePlaceHolder
			case str.Skip:
				dst.Text = T("→ не переводить")
				dst.Style.ColorName = theme.ColorNamePlaceHolder
			default:
				dst.Text = T("→ ещё не переведено")
				dst.Style.ColorName = theme.ColorNamePlaceHolder
			}
			rt.Refresh()
		})
	u.stringsList.OnSelected = func(id widget.ListItemID) { u.openEditor(u.view[id]) }

	// Правка одной строки.
	u.edSource = widget.NewLabel("")
	u.edSource.Wrapping = fyne.TextWrapWord
	u.edSource.Selectable = true
	u.edEntry = widget.NewMultiLineEntry()
	u.edEntry.Wrapping = fyne.TextWrapWord
	u.edEntry.SetMinRowsVisible(3)
	u.edEntry.SetPlaceHolder(T("Свой перевод этой реплики"))
	u.edSave = widget.NewButton(T("Сохранить перевод"), u.saveEdit)
	u.edSave.Importance = widget.HighImportance
	u.edOne = widget.NewButton(T("Перевести эту строку"), u.translateOne)
	closeBtn := widget.NewButton(T("Закрыть"), func() {
		u.showEditor(false)
		u.stringsList.UnselectAll()
	})
	closeBtn.Importance = widget.LowImportance
	u.editor = container.NewVBox(
		widget.NewSeparator(),
		widget.NewLabelWithStyle(T("Оригинал"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.edSource,
		widget.NewLabelWithStyle(T("Перевод"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.edEntry,
		muted(T("Вставки вида {{1:2:…}} — служебные коды игры: иконка кнопки, цвет текста, пауза. Это не ошибка — оставь их в переводе как есть, на нужном месте.")),
		container.NewHBox(u.edSave, u.edOne, closeBtn),
	)
	u.editor.Hide()

	head := container.NewVBox(
		section(T("Реплики игры")),
		muted(T("Галочка — переводить реплику. Нажми на реплику, чтобы поправить перевод вручную. «‹код›» — служебная вставка игры (иконка кнопки, цвет, пауза), это не ошибка.")),
		container.NewBorder(nil, nil, nil, container.NewHBox(u.selAll, u.selNone), u.search),
		u.counter,
	)
	u.stringsPane = container.NewBorder(head, u.editor, nil, nil, u.stringsList)
	return u.stringsPane
}

// showEditor показывает или прячет правку строки; панель надо переложить, иначе редактор получит нулевой размер.
func (u *ui) showEditor(show bool) {
	setVisible(u.editor, show)
	u.stringsPane.Refresh()
}

// refreshStrings пересобирает видимый список по поиску.
func (u *ui) refreshStrings() {
	u.view = u.view[:0]
	if u.session != nil {
		q := strings.ToLower(strings.TrimSpace(u.search.Text))
		for i, s := range u.session.Strings {
			if q == "" || strings.Contains(strings.ToLower(s.Source), q) || strings.Contains(strings.ToLower(s.Translation), q) {
				u.view = append(u.view, i)
			}
		}
	}
	u.stringsList.UnselectAll()
	u.stringsList.Refresh()
	u.updateCounter()
}

func (u *ui) updateCounter() {
	if u.session == nil {
		u.counter.SetText("")
		return
	}
	marked, done := 0, 0
	for _, s := range u.session.Strings {
		if !s.Skip {
			marked++
		}
		if s.Translation != "" {
			done++
		}
	}
	u.counter.SetText(fmt.Sprintf(T("Отмечено %d из %d · переведено %d"), marked, len(u.session.Strings), done))
	u.translateBtn.SetText(fmt.Sprintf(T("3. Перевести отмеченное (%d)"), len(u.session.ToTranslate())+len(u.session.SpeechToTranslate())))
	u.updateEstimate()
}

// markView отмечает или снимает все видимые строки (строки без текста не отмечаются).
func (u *ui) markView(on bool) {
	if u.session == nil {
		return
	}
	for _, i := range u.view {
		s := &u.session.Strings[i]
		s.Skip = !on || !gametext.HasLetters(s.Source)
	}
	u.saveStrings()
	u.stringsList.Refresh()
	u.updateCounter()
}

func (u *ui) saveStrings() {
	if u.session == nil {
		return
	}
	if err := u.session.SaveTranslations(u.toLang()); err != nil {
		u.logf("Не удалось сохранить перевод: %v", err)
	}
}

func (u *ui) openEditor(i int) {
	u.edIdx = i
	s := u.session.Strings[i]
	u.edSource.SetText(s.Source)
	u.edEntry.SetText(s.Translation)
	u.showEditor(true)
}

var editCodeRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

func sortedCodes(s string) []string {
	c := editCodeRe.FindAllString(s, -1)
	sort.Strings(c)
	return c
}

func (u *ui) saveEdit() {
	s := &u.session.Strings[u.edIdx]
	text := strings.TrimSpace(u.edEntry.Text)
	apply := func() {
		s.Translation = text
		if text != "" {
			s.Skip = false
		}
		u.saveStrings()
		u.stringsList.Refresh()
		u.updateCounter()
		u.logf("Перевод сохранён: %s", displayText(text))
	}
	if text != "" && strings.Join(sortedCodes(text), "") != strings.Join(sortedCodes(s.Source), "") {
		dialog.ShowConfirm(T("Служебные коды не совпадают"),
			T("В переводе не те служебные вставки {{…}}, что в оригинале. Игра может показать текст неправильно или упасть. Сохранить всё равно?"),
			func(ok bool) {
				if ok {
					apply()
				}
			}, u.win)
		return
	}
	apply()
}

func (u *ui) translateOne() {
	tr, _, err := u.textTranslator()
	if err != nil {
		u.showError(err)
		return
	}
	src := u.session.Strings[u.edIdx].Source
	u.run(T("Перевод строки"), true, func(ctx context.Context) (func(), error) {
		res, err := tr.Translate(ctx, []string{src})
		if err != nil {
			return nil, err
		}
		return func() { u.edEntry.SetText(res[0]) }, nil
	})
}

// ---------- языки ----------

// fillLocales заполняет выбор языка-оригинала и выбирает подходящий по умолчанию.
func (u *ui) fillLocales() {
	s := u.session
	locs := s.Locales()
	u.localeCodes = nil
	var labels []string
	for _, l := range locs {
		u.localeCodes = append(u.localeCodes, l.Code)
		labels = append(labels, fmt.Sprintf(T("%s (%s), %d реплик"), langName(l.Lang), l.Code, l.Count))
	}
	if len(locs) == 0 {
		setVisible(u.localeSel, false)
		setVisible(u.fromSel, true)
		s.SelectLang(langCode(u.fromSel.Selected))
		u.reloadStrings()
		u.updateFromInfo()
		return
	}
	// язык оригинала задаёт выбранная версия текста — отдельный выбор языка не нужен
	setVisible(u.fromSel, false)
	u.localeSel.Options = labels
	setVisible(u.localeSel, true)
	def := s.DefaultLocale(langCode(u.fromSel.Selected))
	u.localeSel.ClearSelected()
	for i, c := range u.localeCodes {
		if c == def {
			u.localeSel.SetSelectedIndex(i)
		}
	}
}

func (u *ui) onLocale(sel string) {
	if u.session == nil || sel == "" {
		return
	}
	code := u.localeCodes[indexOf(u.localeSel.Options, sel)]
	u.session.SelectLocale(code)
	selectByCode(u.fromSel, gametext.LocaleLang(code))
	u.reloadStrings()
	u.fillAudio() // у другого языка — своя озвучка
	u.updateFromInfo()
}

// updateFromInfo показывает на «Переводе», с какого языка переводим: сам выбор — на «Что нашлось».
func (u *ui) updateFromInfo() {
	if u.fromInfo == nil {
		return
	}
	text := langName(u.fromLang())
	if u.session != nil && u.session.Locale != "" {
		text += " (" + u.session.Locale + ")"
	}
	u.fromInfo.SetText(text + T(" — меняется на вкладке «2. Что нашлось»"))
}

// reloadStrings берёт строки выбранной версии текста и подтягивает уже сделанный перевод на выбранный язык.
func (u *ui) reloadStrings() {
	if u.session == nil || u.stringsList == nil {
		return
	}
	u.session.SelectLocale(u.session.Locale)
	u.session.LoadTranslations(u.toLang())
	u.showEditor(false)
	u.refreshStrings()
	u.updateGlossaryInfo()
	u.refreshGlossary()
}

// ---------- перевод ----------

func (u *ui) updateEstimate() {
	if u.estimate == nil || u.textProv == nil {
		return
	}
	if u.session == nil {
		u.estimate.SetText("")
		return
	}
	strs, speech := len(u.session.ToTranslate()), len(u.session.SpeechToTranslate())
	lines := strs + speech
	todo, _, _ := u.session.TodoTexts()
	chars := 0
	for _, t := range todo {
		chars += len([]rune(t))
	}
	if lines == 0 {
		u.estimate.SetText(T("Всё отмеченное уже переведено."))
		return
	}
	what := fmt.Sprintf(T("%d реплик"), strs)
	if speech > 0 {
		what = fmt.Sprintf(T("%d реплик и речь из %d звуковых файлов"), strs, speech)
	}
	msg := fmt.Sprintf(T("К переводу: %s, %d символов."), what, chars)
	if rep := lines - len(todo); rep > 0 {
		msg = fmt.Sprintf(T("К переводу: %s, из них разных текстов — %d (%d символов): ")+
			T("ещё %d повторяют их и получат тот же перевод бесплатно."), what, len(todo), chars, rep)
	}
	opts := translate.Options()
	o := opts[indexOf(labelsOf(opts), u.textProv.Selected)]
	switch o.ID {
	case "mymemory":
		if chars > 5000 {
			msg += T(" Это больше дневного лимита MyMemory без e-mail (≈5000 символов) — впиши e-mail или переводи частями.")
		}
	case "deepl":
		msg += fmt.Sprintf(T(" Это %.0f%% месячного бесплатного лимита DeepL API Free."), float64(chars)/5000)
	case "google":
		msg += fmt.Sprintf(T(" Примерно $%.2f (около $20 за 1 млн символов)."), float64(chars)*20/1e6)
	case "codex", "claudecode":
		tr, err := translate.New(o.ID, translate.Config{})
		bs, workers := 100, 4
		if err == nil {
			bs, workers = tr.BatchSize(), translate.Workers(tr)
		}
		runs := (len(todo) + bs - 1) / bs
		// замер на Ring Fit: около 0,6 секунды на строку в одном запуске
		minutes := float64(len(todo)) * 0.6 / float64(workers) / 60
		msg += fmt.Sprintf(T(" Это %d запусков по %d строк, по %d одновременно — примерно %s, в счёт лимитов твоей подписки."),
			runs, bs, workers, roughTime(minutes))
	case "claude":
		in, out := map[string]float64{"claude-opus-5-5": 4, "claude-sonnet-5-5": 2, "claude-haiku-5-5": 0.1}, map[string]float64{"claude-opus-5-5": 20, "claude-sonnet-5-5": 10, "claude-haiku-5-5": 0.5}
		if pi, ok := in[u.textModel.Text]; ok {
			// грубо: символ ≈ токен на входе, перевод чуть длиннее; плюс системный промпт на каждую пачку
			inTok := float64(chars)*1.2 + float64(len(todo)/60+1)*600
			outTok := float64(chars) * 1.6
			msg += fmt.Sprintf(T(" Ориентировочно $%.2f без учёта размышлений модели."), (inTok*pi+outTok*out[u.textModel.Text])/1e6)
		}
	}
	u.estimate.SetText(msg)
}

func (u *ui) textTranslator() (translate.Translator, translate.Option, error) {
	opts := translate.Options()
	o := opts[indexOf(labelsOf(opts), u.textProv.Selected)]
	cfg := translate.Config{
		APIKey:  strings.TrimSpace(u.textKey.Text),
		URL:     strings.TrimSpace(u.textURL.Text),
		Model:   strings.TrimSpace(u.textModel.Text),
		From:    u.fromLang(),
		To:      u.toLang(),
		Context: u.gameContext.Text,
	}
	if u.session != nil {
		cfg.Glossary = u.session.GlossaryTerms()
	}
	if cfg.From == cfg.To {
		return nil, o, errors.New(T("языки оригинала и перевода совпадают"))
	}
	u.prefs.SetString("apikey."+vendor(o.ID), cfg.APIKey)
	u.prefs.SetString("url."+o.ID, cfg.URL)
	u.prefs.SetString("model."+o.ID, cfg.Model)
	u.prefs.SetString("context", cfg.Context)
	tr, err := translate.New(o.ID, cfg)
	return tr, o, err
}

func (u *ui) translateText() {
	s := u.session
	if s == nil {
		return
	}
	videos, videoTexts := u.videoToTranslate()
	if len(s.All) == 0 && len(s.SpeechToTranslate()) == 0 && len(videos) == 0 {
		dialog.ShowInformation(T("Нет строк"), T("В этой игре не нашлось строк в поддержанном формате.\nСейчас автоматически извлекается MSBT (игры Nintendo); остальные движки — следующий этап."), u.win)
		return
	}
	todo := s.ToTranslate()
	speech := s.SpeechToTranslate()
	if len(todo)+len(speech)+len(videos) == 0 {
		dialog.ShowInformation(T("Нечего переводить"), T("Все отмеченные реплики, речь и фразы роликов уже переведены. Отметь галочками то, что нужно перевести."), u.win)
		return
	}
	tr, o, err := u.textTranslator()
	if err != nil {
		u.showError(err)
		return
	}
	to := u.toLang()
	start := func() {
		u.run(T("Перевод текста"), true, func(ctx context.Context) (func(), error) {
			done := func() {
				u.refreshStrings()
				u.fillAudio()
				u.fillVideo()
			}
			fail := func(err error) (func(), error) {
				fyne.Do(done)
				return nil, fmt.Errorf(T("%w\nУже переведённое сохранено — повторный запуск продолжит с места остановки"), err)
			}
			if len(todo)+len(speech) > 0 {
				out, err := s.TranslateText(ctx, tr, to, u.progressFn)
				if err != nil {
					return fail(err)
				}
				u.logf("Перевод сохранён: %s", out)
			}
			if len(videos) > 0 {
				if err := s.TranslateVideo(ctx, tr, videos, u.progressFn); err != nil {
					return fail(err)
				}
			}
			return done, nil
		})
	}
	if o.Tier == translate.Paid {
		texts, _, _ := s.TodoTexts()
		texts = append(texts, videoTexts...)
		chars := 0
		for _, t := range texts {
			chars += len([]rune(t))
		}
		dialog.ShowConfirm(T("Платный перевод"),
			fmt.Sprintf(T("В %s уйдёт %d разных текстов (%d символов). Это спишет деньги с твоего аккаунта. Продолжить?"), o.Title, len(texts), chars),
			func(ok bool) {
				if ok {
					start()
				}
			}, u.win)
		return
	}
	start()
}

// roughTime — «около 40 минут», «около 2 ч 10 мин».
func roughTime(minutes float64) string {
	m := int(minutes + 0.5)
	switch {
	case m < 1:
		return T("меньше минуты")
	case m < 60:
		return fmt.Sprintf(T("%d мин"), m)
	}
	return fmt.Sprintf(T("%d ч %d мин"), m/60, m%60/5*5)
}
