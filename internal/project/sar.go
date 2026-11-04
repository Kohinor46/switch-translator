package project

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"switch-translator/internal/audio"
	"switch-translator/internal/codec"
	"switch-translator/internal/engine"
)

// Звуковые архивы со множеством звуков внутри, кроме BARS и FMOD: BFSAR Nintendo (в Kirby — сжатый LZ4,
// *.bfsar.cmpbin) и банки Wwise со встроенными WEM. Строка списка — «архив#звук», как у BARS.

// splitSAR — звук внутри BFSAR: путь архива и имя звука.
func splitSAR(file string) (archive, name string, ok bool) {
	archive, name, ok = strings.Cut(file, barsSep)
	low := strings.ToLower(archive)
	return archive, name, ok && (strings.HasSuffix(low, ".bfsar") || strings.HasSuffix(low, ".bfsar.cmpbin"))
}

// splitBNK — звук WEM внутри банка Wwise.
func splitBNK(file string) (bank, name string, ok bool) {
	bank, name, ok = strings.Cut(file, barsSep)
	return bank, name, ok && strings.EqualFold(path.Ext(bank), ".bnk")
}

// containerReader — файл игры для чтения кусками; сжатый (LZ4, zstd…) распаковывается целиком,
// и последний распакованный запоминается: звуки одного архива читаются подряд.
func (s *Session) containerReader(p string) (io.ReaderAt, int64, error) {
	f, ok := s.romFile(p)
	if !ok {
		return nil, 0, fmt.Errorf("в игре нет файла %s", p)
	}
	r := s.Game.RomFS.Open(f)
	head := make([]byte, 8)
	r.ReadAt(head, 0)
	if codec.Wrapper(head) == "" {
		return r, f.Size, nil
	}
	s.banksMu.Lock()
	defer s.banksMu.Unlock()
	if s.unwrapped.path != p {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, 0, err
		}
		raw, _, err := codec.Unwrap(data)
		if err != nil {
			return nil, 0, err
		}
		s.unwrapped.path, s.unwrapped.data = p, raw
	}
	return bytes.NewReader(s.unwrapped.data), int64(len(s.unwrapped.data)), nil
}

func (s *Session) sarArchive(p string) (*audio.BFSAR, io.ReaderAt, error) {
	r, size, err := s.containerReader(p)
	if err != nil {
		return nil, nil, err
	}
	s.banksMu.Lock()
	a, ok := s.sars[p]
	s.banksMu.Unlock()
	if ok {
		return a, r, nil
	}
	if a, err = audio.ParseBFSAR(r, size); err != nil {
		return nil, nil, err
	}
	s.banksMu.Lock()
	if s.sars == nil {
		s.sars = map[string]*audio.BFSAR{}
	}
	s.sars[p] = a
	s.banksMu.Unlock()
	return a, r, nil
}

func (s *Session) bnkBank(p string) (*audio.BNK, io.ReaderAt, error) {
	r, size, err := s.containerReader(p)
	if err != nil {
		return nil, nil, err
	}
	s.banksMu.Lock()
	b, ok := s.bnks[p]
	s.banksMu.Unlock()
	if ok {
		return b, r, nil
	}
	if b, err = audio.ParseBNK(r, size); err != nil {
		return nil, nil, err
	}
	s.banksMu.Lock()
	if s.bnks == nil {
		s.bnks = map[string]*audio.BNK{}
	}
	s.bnks[p] = b
	s.banksMu.Unlock()
	return b, r, nil
}

// containerSound — байты звука из BFSAR (BFWAV) или банка Wwise (WEM); ok = false — файл не такой.
func (s *Session) containerSound(file string) (data []byte, ok bool, err error) {
	if archive, name, is := splitSAR(file); is {
		a, r, err := s.sarArchive(archive)
		if err != nil {
			return nil, true, err
		}
		i, found := a.Find(name)
		if !found {
			return nil, true, fmt.Errorf("в %s нет звука %s", path.Base(archive), name)
		}
		data, err = a.Wave(r, i)
		return data, true, err
	}
	if bank, name, is := splitBNK(file); is {
		b, r, err := s.bnkBank(bank)
		if err != nil {
			return nil, true, err
		}
		i, found := b.Find(name)
		if !found {
			return nil, true, fmt.Errorf("в %s нет звука %s", path.Base(bank), name)
		}
		data, err = b.Sound(r, i)
		return data, true, err
	}
	return nil, false, nil
}

// addContainerSounds добавляет звуки BFSAR или банка Wwise; false — это не такой архив или в нём нет звуков.
func (s *Session) addContainerSounds(f engine.FileInfo, add func(file, format string, size int64)) bool {
	switch {
	case strings.HasPrefix(f.Format, "BFSAR"):
		a, _, err := s.sarArchive(f.Path)
		if err != nil {
			return false
		}
		format := "звук в архиве " + path.Base(f.Path)
		for _, w := range a.Waves {
			add(f.Path+barsSep+w.Name, format, w.Size())
		}
		return true
	case strings.HasPrefix(f.Format, "Wwise SoundBank"):
		b, _, err := s.bnkBank(f.Path)
		if err != nil {
			return false
		}
		format := "звук в банке Wwise " + path.Base(f.Path)
		for _, w := range b.Sounds {
			add(f.Path+barsSep+w.Name, format, w.Size())
		}
		return true
	}
	return false
}

// writeWaves вписывает озвучку в BFSAR (sars: архив → звук → озвучка) и группы BFGRP внутри архивов SARC
// (groups: «архив::группа.bfgrp» → звук → озвучка). Звук пишется на место оригинала (audio.ReplaceFWAVInPlace),
// поэтому таблицы архивов не меняются; не уместившийся даже на половине частоты — в предупреждениях.
func (s *Session) writeWaves(sars, groups map[string]map[string]*audio.PCM, res *BuildResult) {
	lowered := 0
	put := func(data []byte, waves []audio.SARWave, sounds map[string]*audio.PCM, where string) {
		for name, p := range sounds {
			var err error
			k := -1
			for i, w := range waves {
				if w.Name == name {
					k = i
				}
			}
			if k < 0 {
				err = fmt.Errorf("нет такого звука")
			} else {
				orig := audio.OrigRate(data[waves[k].Offset():])
				var rate int
				if rate, err = audio.ReplaceWave(data, waves[k], p); err == nil && rate < orig {
					lowered++
				}
			}
			if err != nil {
				res.Dubs--
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s#%s: %v", where, name, err))
			}
		}
	}
	for _, archive := range sortedNames(sars) {
		err := s.patchFile(archive, func(raw []byte) ([]byte, error) {
			a, err := audio.ParseBFSAR(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				return nil, err
			}
			put(raw, a.Waves, sars[archive], path.Base(archive))
			return raw, nil
		})
		if err != nil {
			res.Dubs -= len(sars[archive])
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(archive), err))
			continue
		}
		res.Files = appendOnce(res.Files, archive)
		s.log("Озвучка: %s — %d звуков", path.Base(archive), len(sars[archive]))
	}
	// группы — по архивам SARC: в каждом архиве группа за группой
	byOuter := map[string][]string{}
	for g := range groups {
		outer, _, _ := strings.Cut(g, archSep)
		byOuter[outer] = append(byOuter[outer], g)
	}
	for _, outer := range sortedNames(byOuter) {
		sort.Strings(byOuter[outer])
		err := s.patchFile(outer, func(cur []byte) ([]byte, error) {
			for _, g := range byOuter[outer] {
				_, inner, _ := strings.Cut(g, archSep)
				next, err := replaceInArchive(cur, inner, func(grp []byte) ([]byte, error) {
					grp = append([]byte(nil), grp...)
					put(grp, audio.FGRPWaves(grp), groups[g], path.Base(inner))
					return grp, nil
				})
				if err != nil {
					return nil, err
				}
				cur = next
			}
			return cur, nil
		})
		if err != nil {
			for _, g := range byOuter[outer] {
				res.Dubs -= len(groups[g])
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path.Base(outer), err))
			continue
		}
		res.Files = appendOnce(res.Files, outer)
		s.log("Озвучка: %s — групп со звуками: %d", path.Base(outer), len(byOuter[outer]))
	}
	if lowered > 0 {
		s.log("Озвучка: %d звуков длиннее оригинала записаны с пониженной частотой, чтобы уместиться на его место", lowered)
	}
}

// patchFile меняет файл игры: берёт уже собранный в этой сборке (или из игры), снимает сжатие,
// отдаёт fn и сжимает то, что она вернула, тем же.
func (s *Session) patchFile(p string, fn func(raw []byte) ([]byte, error)) error {
	cur, err := os.ReadFile(s.ModPath(p))
	if err != nil {
		if cur, err = s.readRom(p); err != nil {
			return err
		}
	}
	raw, chain, err := codec.Unwrap(cur)
	if err != nil {
		return err
	}
	out, err := fn(append([]byte(nil), raw...))
	if err != nil {
		return err
	}
	if out, err = codec.Wrap(out, chain, cur); err != nil {
		return err
	}
	return writeFile(s.ModPath(p), out)
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
