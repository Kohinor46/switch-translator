package project

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"switch-translator/internal/audio"
	"switch-translator/internal/unity"
)

// Бандлы Unity (Addressables, aa/Switch/*.bundle): озвучка — в узлах CAB-….resource (FSB5),
// по бандлу на язык: «…_localizeaudiodialog_ru_assets_all.bundle».

// bundleNode — содержимое узла бандла из игры.
func (s *Session) bundleNode(bundle, node string) ([]byte, error) {
	data, err := s.readFile(bundle)
	if err != nil {
		return nil, err
	}
	b, err := unity.OpenBundle(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, n := range b.Nodes {
		if n.Path == node {
			return b.ReadNode(bytes.NewReader(data), n)
		}
	}
	return nil, fmt.Errorf("в %s нет %s", bundle, node)
}

// writeBundleSounds вписывает озвучку в узел .resource бандла и правит смещения в AudioClip
// сериализованного узла (тот же путь без «.resource»); бандл пишется в мод без сжатия.
func (s *Session) writeBundleSounds(bundle, node string, r io.ReaderAt, b *audio.FMODBank, dubs map[string]*audio.PCM) error {
	res, moved, err := audio.ReplaceInResource(r, b, dubs)
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(s.ModPath(bundle))
	if err != nil {
		if cur, err = s.readRom(bundle); err != nil {
			return err
		}
	}
	bn, err := unity.OpenBundle(bytes.NewReader(cur), int64(len(cur)))
	if err != nil {
		return err
	}
	replace := map[string][]byte{node: res}
	cab := strings.TrimSuffix(node, ".resource")
	if len(moved) > 0 {
		for _, n := range bn.Nodes {
			if n.Path != cab {
				continue
			}
			data, err := bn.ReadNode(bytes.NewReader(cur), n)
			if err != nil {
				return err
			}
			n, err := unity.PatchAudioClips(data, path.Base(node), moved)
			if err != nil {
				return err
			}
			if n < len(moved) {
				return fmt.Errorf("в %s нашлись описания %d звуков из %d сдвинутых", cab, n, len(moved))
			}
			replace[cab] = data
		}
		if replace[cab] == nil {
			return fmt.Errorf("в бандле нет %s с описаниями звуков", cab)
		}
	}
	out, err := bn.Rebuild(bytes.NewReader(cur), replace)
	if err != nil {
		return err
	}
	return writeFile(s.ModPath(bundle), out)
}

var bundleLangRe = regexp.MustCompile(`localize[a-z]*_([a-z]{2,5})_`)

// bundleLang — язык озвучки бандла по имени ("" — бандл не языковой).
func bundleLang(p string) string {
	m := bundleLangRe.FindStringSubmatch(strings.ToLower(p))
	if m == nil {
		return ""
	}
	switch m[1] {
	case "uk", "us", "en", "gb":
		return "en"
	case "latin", "mx", "mex":
		return "es"
	case "nb":
		return "no"
	}
	return m[1]
}

// addBundleSounds — звуки узлов .resource бандлов; из языковых — только язык оригинала (нет его — английский).
func (s *Session) addBundleSounds(add func(file, format string, size int64)) {
	var bundles []string
	langs := map[string]bool{}
	for _, f := range s.Report.Files {
		if f.Format == "Unity AssetBundle" {
			bundles = append(bundles, f.Path)
			langs[bundleLang(f.Path)] = true
		}
	}
	lang := s.sourceLang()
	if !langs[lang] {
		lang = "en"
	}
	for _, p := range bundles {
		if l := bundleLang(p); l != "" && l != lang {
			continue
		}
		rf, ok := s.romFile(p)
		if !ok {
			continue
		}
		r := s.Game.RomFS.Open(rf)
		b, err := unity.OpenBundle(r, rf.Size)
		if err != nil {
			continue
		}
		for _, n := range b.Nodes {
			if !strings.HasSuffix(n.Path, ".resource") || n.Size == 0 {
				continue
			}
			bank := p + archSep + n.Path
			fb, _, err := s.fmodBank(bank)
			if err != nil {
				continue
			}
			format := "звук в бандле " + path.Base(p)
			for _, smp := range fb.Samples {
				add(bank+barsSep+smp.Name, format, smp.Size())
			}
		}
	}
}

// writeResourceSounds — озвучка в данных звука Unity рядом с ассетами (Data/sharedassets0.resource):
// AudioClip со смещениями лежат в Data/sharedassets0.assets (уже переписанном текстом, если так).
func (s *Session) writeResourceSounds(resPath string, r io.ReaderAt, b *audio.FMODBank, dubs map[string]*audio.PCM, res *BuildResult) error {
	data, moved, err := audio.ReplaceInResource(r, b, dubs)
	if err != nil {
		return err
	}
	if len(moved) > 0 {
		assets := strings.TrimSuffix(resPath, path.Ext(resPath)) + ".assets"
		raw, err := os.ReadFile(s.ModPath(assets))
		if err != nil {
			if raw, err = s.readRom(assets); err != nil {
				return fmt.Errorf("нет %s с описаниями звуков: %w", assets, err)
			}
		}
		n, err := unity.PatchAudioClips(raw, path.Base(resPath), moved)
		if err != nil {
			return err
		}
		if n < len(moved) {
			return fmt.Errorf("в %s нашлись описания %d звуков из %d сдвинутых", path.Base(assets), n, len(moved))
		}
		if err := writeFile(s.ModPath(assets), raw); err != nil {
			return err
		}
		res.Files = appendOnce(res.Files, assets)
	}
	return writeFile(s.ModPath(resPath), data)
}
