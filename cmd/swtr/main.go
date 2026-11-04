// swtr — консольная версия: удобно проверять распаковку без GUI.
//
//	swtr -keys ~/.switch/prod.keys -out ./out game.nsz
//	swtr -keys prod.keys -out ./out -extract text,audio game.nsp
//	swtr -keys prod.keys -out ./out -translate mymemory -from ja -to ru game.nsp
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"switch-translator/internal/engine"
	"switch-translator/internal/keys"
	"switch-translator/internal/nx"
	"switch-translator/internal/project"
	"switch-translator/internal/translate"
)

func main() {
	keysPath := flag.String("keys", os.ExpandEnv("$HOME/.switch/prod.keys"), "путь к prod.keys")
	titleKeys := flag.String("titlekeys", "", "путь к title.keys (необязательно)")
	out := flag.String("out", "", "папка результата (по умолчанию — рядом с образом: «<имя> — перевод»)")
	extract := flag.String("extract", "", "что извлечь: text,audio или all")
	provider := flag.String("translate", "", "переводчик: mymemory, libre, deepl, google, openai, claude")
	apiKey := flag.String("apikey", os.Getenv("TRANSLATE_API_KEY"), "API-ключ переводчика (или переменная TRANSLATE_API_KEY)")
	model := flag.String("model", "", "модель для openai/claude")
	serverURL := flag.String("url", "", "адрес сервера (для libre или совместимого с OpenAI API)")
	from := flag.String("from", "ja", "язык оригинала")
	to := flag.String("to", "ru", "язык перевода")
	locale := flag.String("locale", "", "какую языковую версию игры переводить (USen, EUen…); по умолчанию — по -from")
	extrasFlag := flag.String("extras", "auto", "обновления и DLC (.nsp/.nsz) через «"+string(filepath.ListSeparator)+"»; auto — найти рядом с образом, пусто — без них")
	build := flag.Bool("build", false, "собрать мод (atmosphere/contents/…) из готового перевода")
	glossary := flag.Bool("glossary", false, "перед переводом собрать словарь игры (имена, термины) и перевести его — ИИ-переводчики сверяются с ним")
	image := flag.String("image", "", "собрать образ с переводом: nsp или nsz (игра + своё обновление + DLC)")
	font := flag.String("font", "", "готовый шрифт для мода (FontData.szs, *.bffnt), заменит одноимённый файл игры")
	fit := flag.Bool("fit", true, "подогнать длину озвучки под оригинал: срезать тишину и ускорить длинные фразы")
	glyphs := flag.Bool("glyphs", true, "дорисовать в шрифты игры недостающие буквы (если -font не задан)")
	ttf := flag.String("ttf", "", "откуда рисовать буквы (TTF/OTF); по умолчанию — встроенный Noto Sans Bold")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "использование: swtr [флаги] игра.nsp|игра.nsz")
		flag.PrintDefaults()
		os.Exit(2)
	}

	ks, err := keys.Load(*keysPath)
	check(err)
	if *titleKeys != "" {
		check(ks.LoadTitleKeys(*titleKeys))
	}
	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	last := ""
	progress := func(label string, done, total float64) {
		if total <= 0 {
			return
		}
		p := fmt.Sprintf("%s: %.0f%%", label, done/total*100)
		if p != last {
			fmt.Printf("\r%s   ", p)
			last = p
		}
		if done >= total {
			fmt.Println()
		}
	}

	if *out == "" {
		*out = project.DefaultWorkDir(flag.Arg(0))
	}
	var extras []string
	switch *extrasFlag {
	case "auto":
		extras = project.FindExtras(flag.Arg(0))
	case "":
	default:
		extras = filepath.SplitList(*extrasFlag)
	}
	for _, x := range extras {
		fmt.Println("Дополнение:", filepath.Base(x))
	}
	s, err := project.Analyze(flag.Arg(0), extras, ks, *out, logf, progress)
	check(err)
	defer s.Close()

	fmt.Println("\n== Форматы ==")
	for _, st := range s.Report.Stats() {
		fmt.Printf("  %-9s %6d шт %10s  %s\n", st.Category.Title(), st.Count, nx.HumanSize(st.Size), st.Format)
	}
	for _, e := range s.Report.Engines {
		fmt.Printf("  • %s: %s\n", e.Name, e.Tools)
	}
	fmt.Printf("Отчёт: %s/report.json\n", s.WorkDir)

	if *extract != "" {
		var cats []engine.Category
		if *extract != "all" {
			for _, c := range strings.Split(*extract, ",") {
				cats = append(cats, engine.Category(strings.TrimSpace(c)))
			}
		}
		dir, err := s.ExtractFiles(cats, progress)
		check(err)
		fmt.Println("Извлечено в", dir)
	}

	if *provider != "" {
		if loc := pickLocale(s, *locale, *from); loc != "" {
			s.SelectLocale(loc)
			fmt.Printf("Перевожу текст %s: %d строк\n", loc, len(s.Strings))
		}
		s.LoadTranslations(*to)
		cfg := translate.Config{APIKey: *apiKey, URL: *serverURL, Model: *model, From: *from, To: *to}
		tr, err := translate.New(*provider, cfg)
		check(err)
		if *glossary {
			check(s.BuildGlossary(*to))
			check(s.TranslateGlossary(context.Background(), tr, *to, progress))
			cfg.Glossary = s.GlossaryTerms()
			tr, err = translate.New(*provider, cfg)
			check(err)
		} else if g := s.GlossaryTerms(); len(g) > 0 {
			cfg.Glossary = g
			tr, err = translate.New(*provider, cfg)
			check(err)
		}
		path, err := s.TranslateText(context.Background(), tr, *to, progress)
		check(err)
		fmt.Println("Перевод:", path)
	}

	if *build || *image != "" {
		if *provider == "" {
			if loc := pickLocale(s, *locale, *from); loc != "" {
				s.SelectLocale(loc)
			} else {
				s.SelectLang(*from) // язык выбирает банк озвучки (FMOD)
			}
			s.LoadTranslations(*to)
		}
		bo := project.BuildOptions{To: *to, Audio: true, FitDubs: *fit, Video: true, FontFile: *font, Glyphs: *glyphs, GlyphTTF: *ttf}
		var res *project.BuildResult
		if *image != "" {
			img, err := s.BuildImage(project.ImageOptions{BuildOptions: bo, NSZ: strings.EqualFold(*image, "nsz")}, progress)
			check(err)
			res = img.Mod
			fmt.Printf("Образ: %s (%s)\n", img.Path, nx.HumanSize(img.Size))
			for _, c := range img.Contents {
				fmt.Println("  " + c)
			}
			check(s.VerifyImage(img, progress))
			fmt.Println("  проверка: собранные NCA целы")
		} else {
			var err error
			res, err = s.Build(bo, progress)
			check(err)
		}
		fmt.Printf("Мод: %s\n  реплик: %d, озвучек: %d, роликов: %d, файлов: %d\n", res.Dir, res.Lines, res.Dubs, res.Videos, len(res.Files))
		if len(res.Fonts) > 0 {
			fmt.Printf("  дорисовано букв: %d в шрифтах: %s\n  как выглядит: %s\n", len(res.Drawn), strings.Join(res.Fonts, ", "), res.Preview)
		}
		if len(res.Missing) > 0 {
			fmt.Printf("  в шрифтах нет символов: %s — их нет и в шрифте TTF (-ttf) или нужен готовый шрифт (-font)\n", string(res.Missing))
		}
		for _, w := range res.Warnings {
			fmt.Println("  ⚠", w)
		}
	}
}

func pickLocale(s *project.Session, locale, from string) string {
	if locale != "" {
		return locale
	}
	return s.DefaultLocale(from)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}
