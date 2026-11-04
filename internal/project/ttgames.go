package project

import (
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strings"

	"switch-translator/internal/audio"
	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/ttgames"
)

// Игры LEGO (Traveller's Tales): всё лежит в архивах GAME*.DAT и PATCH.DAT, текст — в таблицах
// stuff\text\text.csv. Один файл бывает в нескольких архивах; игра берёт его из PATCH.DAT,
// поэтому и переводится версия оттуда. Сборка переписывает архив: новые данные и оглавление
// дописываются в конец, остальное копируется как было.

func (s *Session) openDAT(p string) (*ttgames.DAT, error) {
	f, ok := s.romFile(p)
	if !ok {
		return nil, fmt.Errorf("в игре нет файла %s", p)
	}
	return ttgames.Open(s.Game.RomFS.Open(f), f.Size)
}

// datTable — таблица текста: …\stuff\text\*.csv.
func datTable(name string) bool {
	return strings.HasSuffix(name, ".csv") && strings.Contains(name, `stuff\text\`)
}

// datPriority — чем больше, тем главнее архив: PATCH.DAT поверх GAME*.DAT.
func datPriority(p string) int {
	if strings.EqualFold(path.Base(p), "PATCH.DAT") {
		return 1
	}
	return 0
}

// datTexts — строки таблиц текста из всех архивов игры; таблица из нескольких архивов берётся из главного.
func (s *Session) datTexts(files []engine.FileInfo) []gametext.String {
	type src struct {
		dat  string
		prio int
	}
	best := map[string]src{}
	dats := map[string]*ttgames.DAT{}
	for _, f := range files {
		d, err := s.openDAT(f.Path)
		if err != nil {
			s.log("%s: %v", f.Path, err)
			continue
		}
		dats[f.Path] = d
		for _, e := range d.Files {
			if !datTable(e.Name) {
				continue
			}
			if b, ok := best[e.Name]; !ok || datPriority(f.Path) > b.prio {
				best[e.Name] = src{f.Path, datPriority(f.Path)}
			}
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []gametext.String
	for _, n := range names {
		b := best[n]
		d := dats[b.dat]
		rf, _ := s.romFile(b.dat)
		data, err := d.Read(s.Game.RomFS.Open(rf), d.Files[d.Index(n)])
		if err != nil {
			s.log("%s: %v", n, err)
			continue
		}
		out = append(out, gametext.FromTable(b.dat, strings.ReplaceAll(n, `\`, "/"), data)...)
	}
	return out
}

// buildDAT переписывает архив dat с переведёнными таблицами.
func (s *Session) buildDAT(dat string, tr map[string]string, res *BuildResult) error {
	d, err := s.openDAT(dat)
	if err != nil {
		return err
	}
	rf, _ := s.romFile(dat)
	r := s.Game.RomFS.Open(rf)
	replace := map[int][]byte{}
	total := 0
	for i, e := range d.Files {
		if !datTable(e.Name) {
			continue
		}
		inner := strings.ReplaceAll(e.Name, `\`, "/")
		where := path.Join(dat, inner)
		has := false
		for id := range tr {
			if strings.HasPrefix(id, where+"#") {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		data, err := d.Read(r, e)
		if err != nil {
			return err
		}
		nb, n := gametext.ApplyTable(where, data, tr)
		if n == 0 {
			continue
		}
		replace[i] = nb
		total += n
		s.log("Текст: %s — %d реплик", inner, n)
	}
	if total == 0 {
		return nil
	}
	out := s.ModPath(dat)
	if err := os.MkdirAll(path.Dir(out), 0o755); err != nil {
		return err
	}
	w, err := os.Create(out + ".part")
	if err != nil {
		return err
	}
	if err := d.Rewrite(r, w, replace); err != nil {
		w.Close()
		os.Remove(out + ".part")
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.Rename(out+".part", out); err != nil {
		return err
	}
	res.Files = append(res.Files, dat)
	res.Lines += total
	return nil
}

// splitDATSound — реплика внутри архива LEGO: «PATCH.DAT#audio/nnx/vo/…_eng.adpcm».
func splitDATSound(file string) (dat, name string, ok bool) {
	dat, name, ok = strings.Cut(file, barsSep)
	return dat, name, ok && strings.EqualFold(path.Ext(dat), ".dat")
}

// datIndex — оглавление архива (запоминается: у LEGO оно по несколько мегабайт).
func (s *Session) datIndex(p string) (*ttgames.DAT, error) {
	s.banksMu.Lock()
	d, ok := s.dats[p]
	s.banksMu.Unlock()
	if ok {
		return d, nil
	}
	d, err := s.openDAT(p)
	if err != nil {
		return nil, err
	}
	s.banksMu.Lock()
	if s.dats == nil {
		s.dats = map[string]*ttgames.DAT{}
	}
	s.dats[p] = d
	s.banksMu.Unlock()
	return d, nil
}

// datSound — байты реплики из архива.
func (s *Session) datSound(dat, name string) ([]byte, error) {
	d, err := s.datIndex(dat)
	if err != nil {
		return nil, err
	}
	i := d.Index(strings.ReplaceAll(name, "/", `\`))
	if i < 0 {
		return nil, fmt.Errorf("в %s нет %s", dat, name)
	}
	rf, _ := s.romFile(dat)
	return d.Read(s.Game.RomFS.Open(rf), d.Files[i])
}

// addDATSounds — реплики (*.adpcm в audio\) из архивов LEGO: каждая — из главного архива, где она есть,
// а из одной реплики на разных языках («…_eng», «…_fre») — только язык оригинала.
func (s *Session) addDATSounds(add func(file, format string, size int64)) {
	type src struct {
		dat  string
		prio int
		size int64
	}
	best := map[string]src{}
	for _, f := range s.Report.Files {
		if !strings.HasPrefix(f.Format, "TT Games DAT") {
			continue
		}
		d, err := s.datIndex(f.Path)
		if err != nil {
			continue
		}
		for _, e := range d.Files {
			if !strings.HasSuffix(e.Name, ".adpcm") || !strings.HasPrefix(e.Name, `audio\`) {
				continue
			}
			if b, ok := best[e.Name]; !ok || datPriority(f.Path) > b.prio {
				best[e.Name] = src{f.Path, datPriority(f.Path), e.Size}
			}
		}
	}
	// язык озвучки: язык оригинала, если на нём есть реплики, иначе английский
	lang := s.sourceLang()
	tagged := func(n, lang string) bool {
		_, tag, ok := bankTag(n)
		return ok && slices.Contains(bankLangTags[lang], tag)
	}
	found := false
	for n := range best {
		if tagged(n, lang) {
			found = true
			break
		}
	}
	if !found {
		lang = "en"
	}
	var keep []string
	for n := range best {
		if _, _, ok := bankTag(n); !ok || tagged(n, lang) {
			keep = append(keep, n)
		}
	}
	sort.Strings(keep)
	for _, n := range keep {
		b := best[n]
		add(b.dat+barsSep+strings.ReplaceAll(n, `\`, "/"), "реплика в архиве "+path.Base(b.dat), b.size)
	}
}

// writeDATSounds вписывает озвучку в архивы LEGO (архив → реплика → звук); архив, уже
// переписанный текстом в этой сборке, берётся оттуда.
func (s *Session) writeDATSounds(dubs map[string]map[string]*audio.PCM, res *BuildResult) {
	for _, dat := range sortedNames(dubs) {
		err := func() error {
			var r io.ReaderAt
			var size int64
			if f, err := os.Open(s.ModPath(dat)); err == nil {
				defer f.Close()
				st, _ := f.Stat()
				r, size = f, st.Size()
			} else {
				rf, ok := s.romFile(dat)
				if !ok {
					return fmt.Errorf("в игре нет файла %s", dat)
				}
				r, size = s.Game.RomFS.Open(rf), rf.Size
			}
			d, err := ttgames.Open(r, size)
			if err != nil {
				return err
			}
			replace := map[int][]byte{}
			for name, p := range dubs[dat] {
				i := d.Index(strings.ReplaceAll(name, "/", `\`))
				if i < 0 {
					res.Dubs--
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s#%s: нет такой реплики", path.Base(dat), name))
					continue
				}
				orig, err := d.Read(r, d.Files[i])
				if err != nil {
					return err
				}
				nb, err := audio.ReplaceDSPFile(orig, p)
				if err != nil {
					res.Dubs--
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(name), err))
					continue
				}
				replace[i] = nb
			}
			if len(replace) == 0 {
				return nil
			}
			out := s.ModPath(dat)
			if err := os.MkdirAll(path.Dir(out), 0o755); err != nil {
				return err
			}
			w, err := os.Create(out + ".new")
			if err != nil {
				return err
			}
			if err := d.Rewrite(r, w, replace); err != nil {
				w.Close()
				os.Remove(out + ".new")
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
			res.Files = appendOnce(res.Files, dat)
			s.log("Озвучка: %s — %d реплик", path.Base(dat), len(replace))
			return os.Rename(out+".new", out)
		}()
		if err != nil {
			res.Dubs -= len(dubs[dat])
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(dat), err))
		}
	}
}
