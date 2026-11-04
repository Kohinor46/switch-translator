package project

import (
	"fmt"
	"path"
	"strings"

	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/unreal"
)

// Игры на Unreal Engine: тексты — в .locres внутри огромного .pak. Архив не переписывается:
// изменённые .locres складываются в заплатку <архив>_tr_P.pak рядом с ним, а движок
// подключает архивы с «_P» поверх остальных.

// isUnrealPak — файл f разобран как архив .pak Unreal: его строки лежат в .locres внутри,
// и собирает их buildPak. Любой другой файл с расширением .pak (архив другого движка
// с MSBT/SARC внутри) собирается как обычный файл.
func (s *Session) isUnrealPak(f string) bool {
	if s.Report == nil {
		return strings.EqualFold(path.Ext(f), ".pak")
	}
	for _, fi := range s.Report.Files {
		if fi.Path == f {
			return strings.HasPrefix(fi.Format, "Unreal PAK")
		}
	}
	return false
}

func (s *Session) openPak(p string) (*unreal.Pak, error) {
	f, ok := s.romFile(p)
	if !ok {
		return nil, fmt.Errorf("в игре нет файла %s", p)
	}
	return unreal.OpenPak(s.Game.RomFS.Open(f), f.Size)
}

// pakLocres — .locres текстов игры внутри архива (без движковых Engine/…).
func pakLocres(p *unreal.Pak) []unreal.Entry {
	var out []unreal.Entry
	for _, e := range p.Entries {
		rel := strings.TrimPrefix(e.Name, p.MountPoint)
		if strings.HasSuffix(rel, ".locres") && strings.Contains(rel, "/Content/Localization/") && !strings.HasPrefix(rel, "Engine/") {
			out = append(out, e)
		}
	}
	return out
}

// nativeLocres — исходный .locres цели локализации.
type nativeLocres struct {
	culture string
	data    []byte
}

// pakNative — исходные .locres архива по папкам целей локализации (…/Localization/Game):
// исходный язык записан в <цель>.locmeta.
func pakNative(p *unreal.Pak) map[string]nativeLocres {
	out := map[string]nativeLocres{}
	for _, e := range p.Entries {
		if !strings.HasSuffix(e.Name, ".locmeta") || strings.Contains(e.Name, "/Engine/") {
			continue
		}
		b, err := p.Read(e)
		if err != nil {
			continue
		}
		culture, err := unreal.LocMetaNative(b)
		if err != nil || culture == "" {
			continue
		}
		dir := path.Dir(e.Name)
		ne, ok := p.Find(dir + "/" + culture + "/" + path.Base(dir) + ".locres")
		if !ok {
			continue
		}
		if nb, err := p.Read(ne); err == nil {
			out[dir] = nativeLocres{culture, nb}
		}
	}
	return out
}

// nativeFor — исходный .locres для .locres языка e (nil, если e сам исходный или исходного нет).
func nativeFor(natives map[string]nativeLocres, e unreal.Entry) []byte {
	dir := path.Dir(path.Dir(e.Name))
	n, ok := natives[dir]
	if !ok || path.Base(path.Dir(e.Name)) == n.culture {
		return nil
	}
	return n.data
}

// pakTexts — строки всех .locres архива.
func (s *Session) pakTexts(f engine.FileInfo) []gametext.String {
	p, err := s.openPak(f.Path)
	if err != nil {
		s.log("%s: %v", f.Path, err)
		return nil
	}
	var out []gametext.String
	failed := 0
	var lastErr error
	natives := pakNative(p)
	for _, e := range pakLocres(p) {
		b, err := p.Read(e)
		if err != nil {
			failed++
			lastErr = err
			continue
		}
		strs, err := gametext.FromLocres(f.Path, strings.TrimPrefix(e.Name, p.MountPoint), b, nativeFor(natives, e))
		if err != nil {
			s.log("%s: %v", e.Name, err)
			continue
		}
		out = append(out, strs...)
	}
	if failed > 0 {
		s.log("%s: не прочитано файлов с текстом: %d (%v)", path.Base(f.Path), failed, lastErr)
	}
	return out
}

// pakPatchPath — куда кладётся заплатка архива.
func pakPatchPath(pak string) string {
	return strings.TrimSuffix(pak, path.Ext(pak)) + "_tr_P.pak"
}

// buildPak собирает заплатку с переведёнными .locres архива pak.
func (s *Session) buildPak(pak string, tr map[string]string, res *BuildResult) error {
	p, err := s.openPak(pak)
	if err != nil {
		return err
	}
	var names []string
	files := map[string][]byte{}
	total := 0
	// в каких файлах есть переводы: часть ID до «#» (см. gametext.FromLocres)
	translated := map[string]bool{}
	for id := range tr {
		if where, _, ok := strings.Cut(id, "#"); ok {
			translated[where] = true
		}
	}
	natives := pakNative(p)
	for _, e := range pakLocres(p) {
		rel := strings.TrimPrefix(e.Name, p.MountPoint)
		if !translated[path.Join(pak, rel)] {
			continue
		}
		b, err := p.Read(e)
		if err != nil {
			return err
		}
		nb, n, err := gametext.ApplyLocres(path.Join(pak, rel), b, nativeFor(natives, e), tr)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if n == 0 {
			continue
		}
		names = append(names, rel)
		files[rel] = nb
		total += n
		s.log("Текст: %s — %d реплик", rel, n)
	}
	if total == 0 {
		return nil
	}
	out := pakPatchPath(pak)
	if err := writeFile(s.ModPath(out), unreal.WritePak(p.MountPoint, names, files)); err != nil {
		return err
	}
	res.Files = append(res.Files, out)
	res.Lines += total
	return nil
}
