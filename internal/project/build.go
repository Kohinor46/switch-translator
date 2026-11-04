package project

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"switch-translator/internal/audio"
	"switch-translator/internal/bffnt"
	"switch-translator/internal/codec"
	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/nx"
	"switch-translator/internal/translate"
)

// BuildOptions — что положить в мод.
type BuildOptions struct {
	To       string // язык перевода — для имени папки и README
	Audio    bool   // вписать озвучку отмеченных файлов
	FitDubs  bool   // подгонять длину озвучки под оригинал (срезать тишину, ускорить до MaxSpeed)
	Video    bool   // положить пересобранные ролики
	FontFile string // готовый шрифт (FontData.szs, *.bffnt) из другого мода — заменит одноимённый файл игры
	Glyphs   bool   // дорисовать в шрифты игры недостающие буквы (если FontFile не задан)
	GlyphTTF string // откуда рисовать буквы: TTF/OTF; пусто — встроенный Noto Sans Bold
}

// BuildResult — что получилось.
type BuildResult struct {
	Dir      string   // папка мода: внутри atmosphere/contents/<title id>/romfs
	Files    []string // изменённые файлы игры
	Lines    int      // вписано реплик
	Dubs     int      // вписано озвучек
	Sped     int      // озвучек ускорено, чтобы влезть в длину оригинала
	TooLong  int      // озвучек и после ускорения заметно длиннее оригинала
	Videos   int      // переведено роликов
	Missing  []rune   // символы перевода, которых нет ни в одном шрифте игры
	FontsOK  bool     // шрифты игры проверены, все символы перевода в них есть
	FontFile string   // куда лег шрифт, если он подменён
	Fonts    []string // шрифты игры, в которые дорисованы буквы
	Drawn    []rune   // дорисованные буквы
	Preview  string   // картинка: строка перевода новыми шрифтами
	Warnings []string
}

// ModDir — папка мода внутри рабочей папки.
func (s *Session) ModDir() string { return filepath.Join(s.WorkDir, "мод") }

// TitleDir — atmosphere/contents/<title id>: так её ждёт Atmosphere (LayeredFS).
func (s *Session) TitleDir() string {
	return filepath.Join(s.ModDir(), "atmosphere", "contents", fmt.Sprintf("%016X", s.Game.TitleID))
}

// ModPath — куда в моде ложится файл игры. Файлы DLC — в папку своего title id.
func (s *Session) ModPath(file string) string {
	return filepath.Join(s.ModDir(), filepath.FromSlash(modRel(s.Game.TitleID, file)))
}

// modRel — путь в моде относительно его папки: atmosphere/contents/<title id>/romfs/<путь>.
func modRel(app uint64, file string) string {
	tid, inner := app, file
	if t, in, ok := nx.SplitDLC(file); ok {
		tid, inner = t, in
	}
	return fmt.Sprintf("atmosphere/contents/%016X/romfs/%s", tid, inner)
}

// Build собирает мод LayeredFS: только изменённые файлы игры по тем же путям.
// Атмосфера подкладывает их вместо оригинальных — образ игры не меняется.
func (s *Session) Build(opts BuildOptions, progress Progress) (*BuildResult, error) {
	res := &BuildResult{Dir: s.ModDir()}
	if err := os.RemoveAll(s.ModDir()); err != nil {
		return nil, err
	}

	// 1. текст: переводы выбранного языка-оригинала, сгруппированные по файлам
	tr := map[string]string{}
	byFile := map[string]bool{}
	var allText strings.Builder
	for _, st := range s.Strings {
		if st.Translation == "" || st.Skip {
			continue
		}
		tr[st.ID] = st.Translation
		byFile[st.File] = true
		allText.WriteString(st.Translation)
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) == 0 && !opts.Audio && !opts.Video {
		return nil, fmt.Errorf("нет переведённых строк — сначала переведи текст на вкладке «Перевод»")
	}
	for i, f := range files {
		progress("Сборка текста", float64(i), float64(len(files)))
		if strings.EqualFold(path.Ext(f), ".dat") {
			if err := s.buildDAT(f, tr, res); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			continue
		}
		if s.isUnrealPak(f) {
			if err := s.buildPak(f, tr, res); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			continue
		}
		data, err := s.readRom(f)
		if err != nil {
			return nil, err
		}
		out, n, err := gametext.Apply(f, data, tr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if n == 0 {
			continue
		}
		if err := writeFile(s.ModPath(f), out); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, f)
		res.Lines += n
		s.log("Текст: %s — %d реплик", f, n)
	}

	// 2. озвучка
	if opts.Audio {
		// озвучка для банков FMOD, BFSAR и групп BFGRP: каждый пишется один раз со всеми своими звуками
		banks := map[string]map[string]*audio.PCM{}
		sars := map[string]map[string]*audio.PCM{}
		groups := map[string]map[string]*audio.PCM{}
		datDubs := map[string]map[string]*audio.PCM{}
		for i, l := range s.Audio {
			if !l.Selected || l.Dub == "" {
				continue
			}
			progress("Сборка озвучки", float64(i), float64(len(s.Audio)))
			var fit Fit
			if dat, name, ok := splitDATSound(l.File); ok {
				if fit, ok = queueDub(s, i, opts.FitDubs, datDubs, dat, name, res); !ok {
					continue
				}
			} else if archive, name, ok := splitSAR(l.File); ok {
				if fit, ok = queueDub(s, i, opts.FitDubs, sars, archive, name, res); !ok {
					continue
				}
			} else if group, name, ok := splitFGRP(l.File); ok {
				if fit, ok = queueDub(s, i, opts.FitDubs, groups, group, name, res); !ok {
					continue
				}
			} else if bank, name, ok := splitBank(l.File); ok {
				if fit, ok = queueDub(s, i, opts.FitDubs, banks, bank, name, res); !ok {
					continue
				}
			} else if _, _, ok := splitBNK(l.File); ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: вписывать озвучку в банки Wwise программа пока не умеет", path.Base(l.File)))
				continue
			} else {
				file, f, err := s.buildDub(i, opts.FitDubs)
				if err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(l.File), err))
					continue
				}
				if !slices.Contains(res.Files, file) {
					res.Files = append(res.Files, file)
				}
				fit = f
			}
			res.Dubs++
			if fit.Speed > 1.01 {
				res.Sped++
			}
			if fit.TooLong() {
				res.TooLong++
				s.log("Озвучка длиннее оригинала: %s — %s, «%s»", path.Base(l.File), fit.Describe(), l.Translation)
			}
		}
		if len(banks) > 0 {
			progress("Сборка банков FMOD", 0, 1)
			s.writeBanks(banks, res)
		}
		if len(sars) > 0 || len(groups) > 0 {
			progress("Сборка звуковых архивов", 0, 1)
			s.writeWaves(sars, groups, res)
		}
		if len(datDubs) > 0 {
			progress("Сборка архивов .DAT", 0, 1)
			s.writeDATSounds(datDubs, res)
		}
		if opts.FitDubs && res.Dubs > 0 {
			s.log("Подгонка озвучки: ускорено %d из %d (не больше чем в %.1f раза), всё равно длиннее оригинала — %d", res.Sped, res.Dubs, MaxSpeed, res.TooLong)
		}
	}

	// 3. ролики
	if opts.Video {
		if err := s.buildVideos(res); err != nil {
			return nil, err
		}
	}

	// 4. шрифты: свой шрифт или проверка, хватает ли символов в шрифтах игры
	if opts.FontFile != "" {
		dst, err := s.placeFont(opts.FontFile)
		if err != nil {
			return nil, err
		}
		res.FontFile = dst
		res.Files = append(res.Files, dst)
	} else if allText.Len() > 0 {
		patched := map[string][]byte{}
		if opts.Glyphs {
			var err error
			if patched, err = s.addGlyphs(allText.String(), opts, res, progress); err != nil {
				return nil, err
			}
		}
		var checked bool
		res.Missing, checked = s.missingChars(allText.String(), patched)
		res.FontsOK = checked && len(res.Missing) == 0
		if res.FontsOK && len(res.Fonts) == 0 {
			s.log("Шрифты: все буквы перевода уже есть в шрифтах игры — дорисовывать нечего")
		}
	}

	if len(res.Files) == 0 {
		if len(res.Warnings) > 0 {
			return nil, fmt.Errorf("ни один файл игры не изменился: %s", strings.Join(res.Warnings[:min(3, len(res.Warnings))], "; "))
		}
		return nil, fmt.Errorf("ни один файл игры не изменился")
	}
	if err := os.WriteFile(filepath.Join(s.ModDir(), "ПРОЧТИ.txt"), []byte(s.readme(opts, res)), 0o644); err != nil {
		return nil, err
	}
	progress("Готово", 1, 1)
	return res, nil
}

func (s *Session) readRom(file string) ([]byte, error) {
	for _, f := range s.Game.RomFS.Files {
		if f.Path == file {
			return io.ReadAll(s.Game.RomFS.Open(f))
		}
	}
	return nil, fmt.Errorf("в игре нет файла %s", file)
}

// buildDub вписывает озвучку в файл игры и возвращает, какой файл игры изменился.
// Звук может лежать в архиве BARS, а тот — в архиве SARC («пак»): каждый уровень пересобирается,
// и если в этой сборке файл уже менялся, правка ложится поверх.
func (s *Session) buildDub(i int, fit bool) (string, Fit, error) {
	l := s.Audio[i]
	pcm, f, err := s.dubPCM(i, fit)
	if err != nil {
		return "", f, err
	}
	target, sound, inBARS := splitBARS(l.File)
	if !inBARS {
		target = l.File
	}
	replace := func(data []byte) ([]byte, error) {
		if inBARS {
			return audio.ReplaceInBARS(data, sound, pcm)
		}
		return audio.Replace(data, pcm)
	}
	outer, inner, nested := strings.Cut(target, archSep)
	cur, err := os.ReadFile(s.ModPath(outer))
	if err != nil {
		if cur, err = s.readRom(outer); err != nil {
			return "", f, err
		}
	}
	var out []byte
	if !nested {
		// сжатый архив (Mario Wonder: *.bars.zs) — распаковать, вписать и сжать тем же
		raw, chain, uerr := codec.Unwrap(cur)
		if uerr != nil {
			return "", f, uerr
		}
		if out, err = replace(raw); err == nil {
			out, err = codec.Wrap(out, chain, cur)
		}
	} else {
		out, err = replaceInArchive(cur, inner, replace)
	}
	if err != nil {
		return "", f, err
	}
	return outer, f, writeFile(s.ModPath(outer), out)
}

// queueDub откладывает озвучку файла i для архива, который пишется целиком в конце сборки; false — не вышло.
func queueDub(s *Session, i int, fit bool, queue map[string]map[string]*audio.PCM, archive, name string, res *BuildResult) (Fit, bool) {
	pcm, f, err := s.dubPCM(i, fit)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(s.Audio[i].File), err))
		return f, false
	}
	if queue[archive] == nil {
		queue[archive] = map[string]*audio.PCM{}
	}
	queue[archive][name] = pcm
	return f, true
}

// dubPCM — озвучка файла i, какой она ляжет в игру: подогнанная по длине под оригинал, если fit.
func (s *Session) dubPCM(i int, fit bool) (*audio.PCM, Fit, error) {
	if fit {
		return s.FitDub(context.Background(), i)
	}
	b, err := os.ReadFile(s.Audio[i].Dub)
	if err != nil {
		return nil, Fit{}, err
	}
	pcm, err := audio.ReadWAV(b)
	if err != nil {
		return nil, Fit{}, fmt.Errorf("озвучка не в WAV — озвучь заново: %w", err)
	}
	return pcm, Fit{}, nil
}

// replaceInArchive меняет файл inner внутри архива SARC (с сохранением сжатия обоих).
func replaceInArchive(archive []byte, inner string, fn func([]byte) ([]byte, error)) ([]byte, error) {
	files, err := gametext.ArchiveFiles(archive)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Name != inner {
			continue
		}
		raw, wrap, err := codec.Unwrap(f.Data)
		if err != nil {
			return nil, err
		}
		nb, err := fn(raw)
		if err != nil {
			return nil, err
		}
		if nb, err = codec.Wrap(nb, wrap, f.Data); err != nil {
			return nil, err
		}
		return gametext.ReplaceInArchive(archive, inner, nb)
	}
	return nil, fmt.Errorf("в архиве нет файла %s", inner)
}

// placeFont кладёт готовый шрифт вместо одноимённого файла игры (сначала — в папке выбранного языка).
func (s *Session) placeFont(src string) (string, error) {
	name := filepath.Base(src)
	var match []string
	for _, f := range s.Game.RomFS.Files {
		if strings.EqualFold(path.Base(f.Path), name) {
			match = append(match, f.Path)
		}
	}
	if len(match) == 0 {
		return "", fmt.Errorf("в игре нет файла %s — шрифт должен называться так же, как файл игры, который он заменяет", name)
	}
	dst := match[0]
	for _, m := range match {
		if gametext.Locale(m) == s.Locale {
			dst = m
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	return dst, writeFile(s.ModPath(dst), data)
}

// missingChars — символы перевода, которых нет ни в одном шрифте выбранного языка и общих шрифтах.
// patched — шрифты, уже изменённые для мода. checked = false, если шрифтов, которые можно прочитать, нет.
func (s *Session) missingChars(text string, patched map[string][]byte) (missing []rune, checked bool) {
	all := gametext.Font{Chars: map[rune]bool{}}
	for _, f := range s.Report.Of(engine.Font) {
		if loc := gametext.Locale(f.Path); loc != "" && loc != s.Locale {
			continue
		}
		data, ok := patched[f.Path]
		if !ok {
			var err error
			if data, err = s.readRom(f.Path); err != nil {
				continue
			}
		}
		fonts, _ := gametext.Fonts(f.Path, data)
		for _, font := range fonts {
			for r := range font.Chars {
				all.Chars[r] = true
			}
		}
	}
	if len(all.Chars) == 0 {
		return nil, false // шрифтов Nintendo нет — проверить нечем
	}
	return all.Missing(text), true
}

// fontFiles — файлы со шрифтами выбранного языка и общие.
func (s *Session) fontFiles() []string {
	var out []string
	for _, f := range s.Report.Of(engine.Font) {
		if loc := gametext.Locale(f.Path); loc == "" || loc == s.Locale {
			out = append(out, f.Path)
		}
	}
	return out
}

// addGlyphs дорисовывает недостающие буквы во все текстовые шрифты (те, где есть латиница).
// Если перевод на кириллице, добавляется весь русский алфавит — чтобы правки перевода не требовали
// новой сборки шрифта.
func (s *Session) addGlyphs(text string, opts BuildOptions, res *BuildResult, progress Progress) (map[string][]byte, error) {
	ttf := bffnt.DefaultTTF()
	if opts.GlyphTTF != "" {
		var err error
		if ttf, err = os.ReadFile(opts.GlyphTTF); err != nil {
			return nil, err
		}
	}
	chars := []rune(gametext.StripCodes(text))
	for _, r := range chars {
		if unicode.Is(unicode.Cyrillic, r) {
			chars = append(chars, bffnt.Cyrillic...)
			break
		}
	}
	patched := map[string][]byte{}
	drawn := map[rune]bool{}
	var previews []*bffnt.Font
	files := s.fontFiles()
	for i, file := range files {
		progress("Шрифты", float64(i), float64(len(files)))
		data, err := s.readRom(file)
		if err != nil {
			return nil, err
		}
		out, err := gametext.PatchFonts(file, data, func(name string, raw []byte) ([]byte, error) {
			f, err := bffnt.Parse(raw)
			if err != nil || !textFont(f) {
				return nil, nil // не шрифт Switch или не текстовый (значки, цифры)
			}
			r, err := f.Add(chars, bffnt.Options{TTF: ttf, Reuse: true})
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("Шрифт %s: %v", path.Base(name), err))
				return nil, nil
			}
			if r.Data == nil {
				return nil, nil
			}
			for _, c := range r.Drawn {
				drawn[c] = true
			}
			if g, err := bffnt.Parse(r.Data); err == nil {
				previews = append(previews, g)
			}
			res.Fonts = append(res.Fonts, name)
			s.log("Шрифт %s: дорисовано %d букв, взято у латиницы %d, листов +%d", name, len(r.Drawn), len(r.Reused), r.Sheets)
			return r.Data, nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if out == nil {
			continue
		}
		if err := writeFile(s.ModPath(file), out); err != nil {
			return nil, err
		}
		patched[file] = out
		res.Files = append(res.Files, file)
	}
	for c := range drawn {
		res.Drawn = append(res.Drawn, c)
	}
	slices.Sort(res.Drawn)
	if len(previews) > 0 {
		p := filepath.Join(s.WorkDir, "шрифты.png")
		if err := savePreview(p, previews, s.sampleLine()); err == nil {
			res.Preview = p
		}
	}
	return patched, nil
}

// textFont — шрифт для текста: в нём есть почти вся латиница, а не только цифры и значки.
func textFont(f *bffnt.Font) bool {
	n := 0
	for c := range f.Chars() {
		if c < 0x80 && unicode.IsLetter(c) {
			n++
		}
	}
	return n >= 40
}

// sampleLine — строка перевода для предпросмотра: та, где больше всего кириллицы.
func (s *Session) sampleLine() string {
	best, score := "", 0
	for _, st := range s.Strings {
		if st.Skip {
			continue
		}
		t := []rune(strings.Join(strings.Fields(gametext.StripCodes(st.Translation)), " "))
		if len(t) > 48 {
			t = t[:48]
		}
		n := 0
		for _, r := range t {
			if unicode.Is(unicode.Cyrillic, r) {
				n++
			}
		}
		if n > score {
			best, score = string(t), n
		}
	}
	if score < 12 {
		return "Съешь же ещё этих мягких французских булок, да выпей чаю!"
	}
	return best
}

// savePreview набирает строку каждым изменённым шрифтом — белым по тёмному, как в игре.
func savePreview(p string, fonts []*bffnt.Font, line string) error {
	var rows []*image.NRGBA
	w, h := 0, 0
	for _, f := range fonts {
		img := f.Render(line)
		rows = append(rows, img)
		w = max(w, img.Rect.Dx())
		h += img.Rect.Dy() + 8
	}
	out := image.NewNRGBA(image.Rect(0, 0, w+16, h+8))
	draw.Draw(out, out.Rect, &image.Uniform{color.NRGBA{58, 52, 110, 255}}, image.Point{}, draw.Src)
	y := 8
	for _, img := range rows {
		draw.Draw(out, img.Rect.Add(image.Pt(8, y)), img, image.Point{}, draw.Over)
		y += img.Rect.Dy() + 8
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o644)
}

func (s *Session) readme(opts BuildOptions, res *BuildResult) string {
	var b strings.Builder
	tid := fmt.Sprintf("%016X", s.Game.TitleID)
	fmt.Fprintf(&b, "Перевод: %s\nСобрано: %s программой Switch Translator\n\n", filepath.Base(s.Game.Path), time.Now().Format("02.01.2006 15:04"))
	b.WriteString("УСТАНОВКА (консоль с Atmosphere)\n\n")
	fmt.Fprintf(&b, "1. Скопируй папку atmosphere на карту памяти, в корень. Получится путь\n   atmosphere/contents/%s/romfs/…\n", tid)
	fmt.Fprintf(&b, "2. В настройках консоли выбери язык %s — перевод заменяет именно его.\n", ConsoleLanguage(s.Locale))
	b.WriteString("3. Запусти игру. Чтобы вернуть оригинал, удали папку " + tid + ".\n\n")
	if s.Game.UpdatePath != "" {
		ver := fmt.Sprintf("v%d", s.Game.Version)
		if s.Game.Display != "" {
			ver = s.Game.Display + " (" + ver + ")"
		}
		fmt.Fprintf(&b, "Мод собран для версии игры %s. На консоли должна стоять эта же версия:\n"+
			"с другой версией тексты могут не совпасть.\n\n", ver)
	} else {
		b.WriteString("Мод собран для версии игры без обновлений. Если на консоли стоит обновление,\n" +
			"открой в программе и его — иначе новые тексты из обновления пропадут.\n\n")
	}
	b.WriteString("Изменённые файлы:\n")
	for _, f := range res.Files {
		b.WriteString("  " + strings.TrimPrefix(modRel(s.Game.TitleID, f), "atmosphere/contents/") + "\n")
	}
	return b.String()
}

// consoleLanguages — как язык называется в настройках Switch.
var consoleLanguages = map[string]string{
	"USen": "English (United States)", "EUen": "English (United Kingdom)",
	"USfr": "Français (Canada)", "EUfr": "Français",
	"USes": "Español (Latinoamérica)", "EUes": "Español (España)",
	"EUde": "Deutsch", "EUit": "Italiano", "EUnl": "Nederlands", "EUru": "Русский",
	"EUpt": "Português", "USpt": "Português (Brasil)",
	"JPja": "日本語", "KRko": "한국어", "CNzh": "简体中文", "TWzh": "繁體中文",
}

// ConsoleLanguage — какой язык выбрать на консоли, чтобы увидеть перевод.
func ConsoleLanguage(loc string) string {
	if loc == "" {
		return "оригинала игры"
	}
	name := translate.LanguageName(gametext.LocaleLang(loc))
	if c, ok := consoleLanguages[loc]; ok {
		return fmt.Sprintf("«%s» (%s, %s)", c, name, loc)
	}
	return fmt.Sprintf("%s (%s)", name, loc)
}

func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
