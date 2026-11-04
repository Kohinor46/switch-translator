package project

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"switch-translator/internal/audio"
	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/nx"
)

// Звук игр на FMOD (Unreal, Unity…): банки .bank с тысячами звуков. Каждый звук — отдельная строка
// «PPA/Content/FMOD/Switch/Dialogues_ENG.bank#CH_02_Playground_SPK_21_ENG»; из банков озвучки на разных
// языках («Dialogues_ENG», «Dialogues_FRA»…) показывается только банк языка оригинала.

// SelectLang задаёт язык оригинала для игр, где в путях нет кодов языков Nintendo (USen, JPja…):
// по нему выбирается банк озвучки этого языка.
func (s *Session) SelectLang(code string) {
	if code == s.Lang {
		return
	}
	s.Lang = code
	if s.Report != nil {
		s.initAudio()
	}
}

// sourceLang — язык оригинала: из выбранной версии текста или заданный SelectLang.
func (s *Session) sourceLang() string {
	if s.Locale != "" {
		return gametext.LocaleLang(s.Locale) // USen → en, en-US → en
	}
	return s.Lang
}

// bankLangTags — как языки подписывают в именах банков, от основного варианта к запасному.
var bankLangTags = map[string][]string{
	"en": {"ENG", "EN", "USA", "US", "EUK", "UK", "GB"},
	"fr": {"FRA", "FRE", "FR", "FRC"},
	"de": {"GER", "DEU", "DE"},
	"it": {"ITA", "IT"},
	"es": {"SPA", "ESP", "ES", "MEX", "LAT"},
	"ja": {"JPN", "JAP", "JP", "JA"},
	"pt": {"BRA", "POR", "PTB", "PT", "BR"},
	"nl": {"DUT", "NLD", "NL"},
	"da": {"DAN", "DA"},
	"fi": {"FIN", "FI"},
	"no": {"NOR", "NO"},
	"sv": {"SWE", "SV"},
	"pl": {"POL", "PL"},
	"ar": {"ARA", "AR"},
	"ru": {"RUS", "RU"},
	"ko": {"KOR", "KO", "KR"},
	"zh": {"CHN", "CHS", "CHT", "ZHO", "ZH"},
	"tr": {"TUR", "TR"},
	"cs": {"CZE", "CS"},
	"hu": {"HUN", "HU"},
	"el": {"GRE", "EL"},
}

// bankTag — имя банка без языка и сам язык: «Dialogues_ENG.bank» → «Dialogues», «ENG».
func bankTag(p string) (stem, tag string, ok bool) {
	base := strings.TrimSuffix(p, path.Ext(p))
	i := strings.LastIndexAny(base, "_-.")
	if i <= 0 {
		return "", "", false
	}
	tag = strings.ToUpper(base[i+1:])
	for _, tags := range bankLangTags {
		for _, t := range tags {
			if t == tag {
				return base[:i], tag, true
			}
		}
	}
	return "", "", false
}

// otherLangBanks — банки озвучки других языков, которые не показываем: из каждой группы
// одноимённых банков на разных языках остаётся банк языка оригинала (нет такого — английский).
func (s *Session) otherLangBanks(files []engine.FileInfo) map[string]bool {
	groups := map[string][]string{}
	for _, f := range files {
		if stem, _, ok := bankTag(f.Path); ok && strings.HasPrefix(f.Format, "FMOD") {
			groups[stem] = append(groups[stem], f.Path)
		}
	}
	skip := map[string]bool{}
	for _, paths := range groups {
		if len(paths) < 2 {
			continue // один банк — не выбор языка, а просто имя
		}
		sort.Strings(paths)
		keep := pickLangBank(paths, s.sourceLang())
		if keep == "" {
			keep = pickLangBank(paths, "en")
		}
		if keep == "" {
			keep = paths[0]
		}
		for _, p := range paths {
			if p != keep {
				skip[p] = true
			}
		}
	}
	return skip
}

func pickLangBank(paths []string, lang string) string {
	for _, want := range bankLangTags[lang] {
		for _, p := range paths {
			if _, tag, _ := bankTag(p); tag == want {
				return p
			}
		}
	}
	return ""
}

// splitBank — звук внутри банка FMOD: путь банка и имя звука.
func splitBank(file string) (bank, name string, ok bool) {
	bank, name, ok = strings.Cut(file, barsSep)
	ext := strings.ToLower(path.Ext(bank))
	return bank, name, ok && (ext == ".bank" || ext == ".fsb" || ext == ".resource")
}

func (s *Session) romFile(p string) (nx.RomFile, bool) {
	for _, f := range s.Game.RomFS.Files {
		if f.Path == p {
			return f, true
		}
	}
	return nx.RomFile{}, false
}

// fmodBank — оглавление банка (читается из образа кусками и запоминается: банки бывают по сотне мегабайт).
// Банк бывает и узлом бандла Unity: «…/x.bundle::CAB-….resource».
func (s *Session) fmodBank(p string) (*audio.FMODBank, io.ReaderAt, error) {
	r, size, err := s.bankReader(p)
	if err != nil {
		return nil, nil, err
	}
	s.banksMu.Lock()
	defer s.banksMu.Unlock()
	if b, ok := s.banks[p]; ok {
		return b, r, nil
	}
	b, err := audio.ParseFMOD(r, size)
	if err != nil {
		return nil, nil, err
	}
	if s.banks == nil {
		s.banks = map[string]*audio.FMODBank{}
	}
	s.banks[p] = b
	return b, r, nil
}

// bankReader — данные банка: файл игры или узел бандла Unity.
func (s *Session) bankReader(p string) (io.ReaderAt, int64, error) {
	if outer, node, ok := strings.Cut(p, archSep); ok {
		data, err := s.bundleNode(outer, node)
		if err != nil {
			return nil, 0, err
		}
		return bytes.NewReader(data), int64(len(data)), nil
	}
	f, ok := s.romFile(p)
	if !ok {
		return nil, 0, fmt.Errorf("в игре нет файла %s", p)
	}
	return s.Game.RomFS.Open(f), f.Size, nil
}

// bankSound — звук из банка отдельным файлом FSB5.
func (s *Session) bankSound(bank, name string) ([]byte, error) {
	b, r, err := s.fmodBank(bank)
	if err != nil {
		return nil, err
	}
	i, ok := b.Find(name)
	if !ok {
		return nil, fmt.Errorf("в %s нет звука %s", path.Base(bank), name)
	}
	return b.Sample(r, i)
}

// addBankSounds добавляет в список звука звуки банка; false — банк не прочитался.
func (s *Session) addBankSounds(f engine.FileInfo, add func(file, format string, size int64)) bool {
	b, _, err := s.fmodBank(f.Path)
	if err != nil {
		return false
	}
	format := "звук в банке FMOD " + path.Base(f.Path)
	for _, smp := range b.Samples {
		add(f.Path+barsSep+smp.Name, format, smp.Size())
	}
	return true
}

// writeBanks вписывает озвучку в банки FMOD — каждый банк пересобирается один раз со всеми своими звуками.
func (s *Session) writeBanks(dubs map[string]map[string]*audio.PCM, res *BuildResult) {
	banks := make([]string, 0, len(dubs))
	for b := range dubs {
		banks = append(banks, b)
	}
	sort.Strings(banks)
	for _, bank := range banks {
		b, r, err := s.fmodBank(bank)
		file := bank
		if err == nil {
			if outer, node, ok := strings.Cut(bank, archSep); ok {
				// звук в бандле Unity: данные .resource и смещения в AudioClip узла CAB рядом
				file = outer
				err = s.writeBundleSounds(outer, node, r, b, dubs[bank])
			} else if strings.EqualFold(path.Ext(bank), ".resource") {
				// данные звука Unity рядом с ассетами: смещения — в AudioClip файла <имя>.assets
				err = s.writeResourceSounds(bank, r, b, dubs[bank], res)
			} else {
				var out []byte
				if out, err = audio.ReplaceInFMOD(r, b, dubs[bank]); err == nil {
					err = writeFile(s.ModPath(bank), out)
				}
			}
		}
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(bank), err))
			res.Dubs -= len(dubs[bank])
			continue
		}
		res.Files = appendOnce(res.Files, file)
		s.log("Озвучка: %s — %d звуков", path.Base(bank), len(dubs[bank]))
	}
}
