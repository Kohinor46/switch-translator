package project

import (
	"bytes"
	"context"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"switch-translator/internal/audio"
	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/keys"
	"switch-translator/internal/nx"
	"switch-translator/internal/ttgames"
	"switch-translator/internal/unity"
	"switch-translator/internal/unreal"
	"switch-translator/internal/video"
)

// Прогон по всем играм папки: SWTR_ALL=<папка с играми> (SWTR_ONLY=Kirby,Wonder — только эти). Текст, звук и видео — что нашлось и что нет.
func TestAllGamesReport(t *testing.T) {
	root := os.Getenv("SWTR_ALL")
	if root == "" {
		t.Skip("SWTR_ALL не задан")
	}
	home, _ := os.UserHomeDir()
	ks, err := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	if err != nil {
		t.Fatal(err)
	}
	var games []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		ext := strings.ToLower(filepath.Ext(p))
		if err != nil || d.IsDir() || (ext != ".nsp" && ext != ".nsz") {
			return nil
		}
		if tid, ok := nameTitleID(filepath.Base(p)); ok && tid&0xFFF == 0 && !strings.Contains(p, "перевод") {
			games = append(games, p)
		}
		return nil
	})
	sort.Strings(games)
	if only := os.Getenv("SWTR_ONLY"); only != "" {
		var keep []string
		for _, g := range games {
			for _, o := range strings.Split(only, ",") {
				if strings.Contains(g, o) {
					keep = append(keep, g)
				}
			}
		}
		games = keep
	}
	for _, g := range games {
		fmt.Printf("\n######## %s\n", filepath.Base(g))
		work := t.TempDir()
		began := time.Now()
		var errs []string
		s, err := Analyze(g, FindExtras(g), ks, work, func(f string, a ...any) {
			if m := fmt.Sprintf(f, a...); strings.Contains(m, ": ") && !strings.HasPrefix(m, "Движок") && !strings.HasPrefix(m, "Текст:") {
				errs = append(errs, m)
			}
		}, func(string, float64, float64) {})
		if err != nil {
			fmt.Printf("  ОШИБКА: %v\n", err)
			continue
		}
		for _, e := range s.Report.Engines {
			fmt.Printf("  движок: %s\n", e.Name)
		}
		if sub := os.Getenv("SWTR_FMT"); sub != "" {
			n := 0
			for _, f := range s.Report.Files {
				if strings.Contains(f.Path, sub) && n < 5 {
					fmt.Printf("    %s: %s [%s] %s\n", f.Path, f.Format, f.Wrapper, f.Category)
					n++
				}
			}
		}
		fmt.Printf("  анализ %v, файлов %d\n", time.Since(began).Round(time.Second), len(s.Report.Files))

		// текст
		perFile := map[string]int{}
		for _, str := range s.All {
			perFile[str.File]++
		}
		empty := map[string]int{}
		textFiles := s.Report.Of(engine.Text)
		for _, f := range textFiles {
			if perFile[f.Path] == 0 && !strings.Contains(f.Format, "MSBP") && !strings.Contains(f.Format, "MSBF") {
				empty[f.Format]++
			}
		}
		fmt.Printf("  ТЕКСТ: файлов %d, строк %d\n", len(textFiles), len(s.All))
		var locs []string
		for _, l := range s.Locales() {
			locs = append(locs, fmt.Sprintf("%s:%d", l.Code, l.Count))
		}
		if len(locs) > 0 {
			fmt.Printf("    языки текста: %s\n", strings.Join(locs, " "))
		}
		noLoc := 0
		for _, str := range s.All {
			if str.Locale == "" {
				noLoc++
			}
		}
		if noLoc > 0 {
			fmt.Printf("    строк без языка в пути: %d\n", noLoc)
			seen := map[string]int{}
			for _, str := range s.All {
				if str.Locale == "" {
					seen[str.File+" :: "+str.Inner]++
				}
			}
			n := 0
			for _, k := range sortedNames(seen) {
				if n++; n > 12 {
					break
				}
				fmt.Printf("      %s (%d)\n", k, seen[k])
			}
		}
		for f, n := range empty {
			fmt.Printf("    без строк: %s × %d\n", f, n)
		}
		for i, e := range errs {
			if i == 5 {
				fmt.Printf("    … ещё ошибок: %d\n", len(errs)-5)
				break
			}
			fmt.Printf("    ошибка: %s\n", e)
		}
		if loc := s.DefaultLocale("en"); loc != "" {
			s.SelectLocale(loc)
			fmt.Printf("    для перевода (%s): %d строк\n", loc, len(s.Strings))
		} else {
			s.SelectLang("en")
		}

		// звук
		fmt.Printf("  ЗВУК: строк %d\n", len(s.Audio))
		aFmt := map[string]int{}
		for _, f := range s.Report.Of(engine.Audio) {
			aFmt[f.Format]++
		}
		for f, n := range aFmt {
			fmt.Printf("    %s × %d\n", f, n)
		}

		// видео
		fmt.Printf("  ВИДЕО: роликов %d\n", len(s.Video))
		vFmt := map[string]int{}
		unsup := 0
		for _, v := range s.Video {
			vFmt[v.Format]++
			if v.Unsupported != "" {
				unsup++
			}
		}
		for f, n := range vFmt {
			fmt.Printf("    %s × %d\n", f, n)
		}
		if unsup > 0 {
			fmt.Printf("    не пересобрать: %d\n", unsup)
		}
		// пробуем открыть самый маленький ролик каждого формата
		tried := map[string]bool{}
		vs := append([]VideoLine(nil), s.Video...)
		sort.Slice(vs, func(i, j int) bool { return vs[i].Size < vs[j].Size })
		for _, v := range vs {
			if tried[v.Format] || v.Size > 400<<20 {
				continue
			}
			tried[v.Format] = true
			src, err := s.VideoSource(v.File)
			if err != nil {
				fmt.Printf("    %s: не извлечь: %v\n", path.Base(v.File), err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			info, err := video.Probe(ctx, src)
			cancel()
			if err != nil {
				fmt.Printf("    %s: ffprobe: %v\n", path.Base(v.File), err)
			} else {
				fmt.Printf("    %s: %s\n", path.Base(v.File), info.Describe())
			}
			os.Remove(src)
		}

		// неизвестное — вдруг там текст, звук или видео
		unk := map[string]int{}
		var unkSize int64
		for _, f := range s.Report.Of(engine.Unknown) {
			unk[strings.ToLower(path.Ext(f.Path))]++
			unkSize += f.Size
		}
		if len(unk) > 0 {
			var top []string
			for e, n := range unk {
				top = append(top, fmt.Sprintf("%s×%d", e, n))
			}
			sort.Slice(top, func(i, j int) bool { return top[i] < top[j] })
			if len(top) > 12 {
				top = top[:12]
			}
			fmt.Printf("  НЕИЗВЕСТНО: %d МБ: %s\n", unkSize>>20, strings.Join(top, " "))
		}
		s.Close()
	}
}

// Список файлов игры и извлечение нужных: SWTR_DUMP=<образ>, SWTR_DUMP_OUT=<папка>, SWTR_DUMP_PAT=<regexp пути>.
func TestDumpGame(t *testing.T) {
	game, out := os.Getenv("SWTR_DUMP"), os.Getenv("SWTR_DUMP_OUT")
	if game == "" || out == "" {
		t.Skip("SWTR_DUMP не задан")
	}
	home, _ := os.UserHomeDir()
	ks, err := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	g, err := nx.OpenGame(game, FindExtras(game), ks, tmp, t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pat := regexp.MustCompile(os.Getenv("SWTR_DUMP_PAT"))
	os.MkdirAll(out, 0o755)
	var list strings.Builder
	for _, f := range g.RomFS.Files {
		fmt.Fprintf(&list, "%12d %s\n", f.Size, f.Path)
		if os.Getenv("SWTR_DUMP_PAT") != "" && pat.MatchString(f.Path) {
			dst := filepath.Join(out, "files", f.Path)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := copyOut(g.RomFS.Open(f), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	os.WriteFile(filepath.Join(out, "list.txt"), []byte(list.String()), 0o644)
}

// Сборка текста игры: SWTR_TEXT_GAME=<образ>. Первые 50 строк выбранного языка переводятся, мод собирается,
// и в собранных файлах находятся переводы.
func TestTextBuildGame(t *testing.T) {
	game := os.Getenv("SWTR_TEXT_GAME")
	if game == "" {
		t.Skip("SWTR_TEXT_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, err := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	s, err := Analyze(game, FindExtras(game), ks, work, t.Logf, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SelectLocale(s.DefaultLocale("en"))
	want := map[string]string{}
	step := max(1, len(s.Strings)/50) // строки со всего списка, а не только первые
	for i := 0; i < len(s.Strings); i += step {
		if len(want) == 50 {
			break
		}
		if s.Strings[i].Skip {
			continue
		}
		s.Strings[i].Translation = "Перевод №" + strconv.Itoa(i) + ", «" + s.Strings[i].Source + "»"
		want[s.Strings[i].ID] = s.Strings[i].Translation
	}
	res, err := s.Build(BuildOptions{To: "ru"}, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines != len(want) {
		t.Fatalf("вписано %d строк из %d; %v", res.Lines, len(want), res.Warnings)
	}
	found := 0
	for _, f := range res.Files {
		b, err := os.ReadFile(s.ModPath(f))
		if err != nil {
			t.Fatal(err)
		}
		var strs []gametext.String
		if strings.HasSuffix(f, ".DAT") { // архив LEGO
			d, err := ttgames.Open(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatalf("собранный %s не читается: %v", f, err)
			}
			for _, e := range d.Files {
				if strings.HasSuffix(e.Name, ".csv") && strings.Contains(e.Name, `stuff\text\`) {
					data, err := d.Read(bytes.NewReader(b), e)
					if err != nil {
						t.Fatal(err)
					}
					strs = append(strs, gametext.FromTable(f, strings.ReplaceAll(e.Name, `\`, "/"), data)...)
				}
			}
		} else if orig, ok := strings.CutSuffix(f, "_tr_P.pak"); ok { // заплатка архива Unreal
			pp, err := unreal.OpenPak(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range pp.Entries {
				data, _ := pp.Read(e)
				ss, err := gametext.FromLocres(orig+".pak", strings.TrimPrefix(e.Name, pp.MountPoint), data, nil)
				if err != nil {
					t.Fatal(err)
				}
				strs = append(strs, ss...)
			}
		} else if strs, err = gametext.Extract(f, b); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, st := range strs {
			if w, ok := want[st.ID]; ok {
				if st.Source != w {
					t.Fatalf("%s: %q, ждали %q", st.ID, st.Source, w)
				}
				found++
			}
		}
	}
	if found != len(want) {
		t.Fatalf("в собранных файлах нашлось %d переводов из %d", found, len(want))
	}
	t.Logf("файлы: %v, предупреждения: %v", res.Files, res.Warnings)
}

// Оглавление .pak игры на Unreal: SWTR_PAK_GAME=<образ>; SWTR_PAK_PAT — какие файлы показать (regexp).
func TestPakGame(t *testing.T) {
	game := os.Getenv("SWTR_PAK_GAME")
	if game == "" {
		t.Skip("SWTR_PAK_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, err := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := nx.OpenGame(game, FindExtras(game), ks, t.TempDir(), t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pat := regexp.MustCompile(os.Getenv("SWTR_PAK_PAT"))
	for _, f := range g.RomFS.Files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".pak") {
			continue
		}
		p, err := unreal.OpenPak(g.RomFS.Open(f), f.Size)
		if p == nil {
			t.Fatalf("%s: %v", f.Path, err)
		}
		fmt.Printf("%s: версия %d, точка %q, файлов %d, сжатие %v, шифрован %v %v\n", f.Path, p.Version, p.MountPoint, len(p.Entries), p.Compression, p.Encrypted, err)
		exts := map[string]int{}
		for _, e := range p.Entries {
			exts[strings.ToLower(path.Ext(e.Name))]++
			if os.Getenv("SWTR_PAK_PAT") != "" && pat.MatchString(e.Name) {
				fmt.Printf("   %s %d/%d c%d e%v блоков %d по %d\n", e.Name, e.Size, e.RawSize, e.Compression, e.Encrypted, len(e.Blocks), e.BlockSize)
				if out := os.Getenv("SWTR_PAK_OUT"); out != "" && len(e.Blocks) > 0 {
					// сжатые блоки по одному файлу: <n>_<блок>_<исходный размер>.bin
					for i, bl := range e.Blocks {
						raw := int64(e.BlockSize)
						if i == len(e.Blocks)-1 {
							raw = e.RawSize - int64(i)*int64(e.BlockSize)
						}
						buf := make([]byte, bl[1]-bl[0])
						g.RomFS.Open(f).ReadAt(buf, e.Offset+bl[0])
						name := fmt.Sprintf("%s_%d_%d.bin", strings.ReplaceAll(path.Base(e.Name), ".", "_"), i, raw)
						os.WriteFile(filepath.Join(out, name), buf, 0o644)
					}
				}
				if len(e.Blocks) > 0 {
					head := make([]byte, 16)
					g.RomFS.Open(f).ReadAt(head, e.Offset+e.Blocks[0][0])
					fmt.Printf("      % x\n", head)
				}
			}
		}
		fmt.Println("  ", exts)
	}
}

// .locres игры на Unreal: SWTR_PAK_GAME=<образ>. Каждый .locres из Content/Localization читается,
// собирается снова в те же строки; заплатка .pak с переписанным файлом читается обратно.
func TestLocresGame(t *testing.T) {
	game := os.Getenv("SWTR_PAK_GAME")
	if game == "" {
		t.Skip("SWTR_PAK_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	g, err := nx.OpenGame(game, FindExtras(game), ks, t.TempDir(), t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, f := range g.RomFS.Files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".pak") {
			continue
		}
		p, err := unreal.OpenPak(g.RomFS.Open(f), f.Size)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Entries {
			if !strings.HasSuffix(e.Name, ".locres") || strings.Contains(e.Name, "/Engine/") {
				continue
			}
			b, err := p.Read(e)
			if err != nil {
				t.Logf("%s: %v", e.Name, err)
				continue
			}
			l, err := unreal.ParseLocres(b)
			if err != nil {
				t.Fatalf("%s: %v", e.Name, err)
			}
			again, err := unreal.ParseLocres(l.Bytes())
			if err != nil || len(again.Texts) != len(l.Texts) {
				t.Fatalf("%s: пересобранный не читается: %v", e.Name, err)
			}
			for i := range l.Texts {
				if again.Texts[i] != l.Texts[i] {
					t.Fatalf("%s: %+v ≠ %+v", e.Name, again.Texts[i], l.Texts[i])
				}
			}
			sample := ""
			if len(l.Texts) > 10 {
				sample = l.Texts[10].Namespace + "/" + l.Texts[10].Key + " = " + l.Texts[10].Text
			}
			fmt.Printf("%s: версия %d, строк %d; %s\n", e.Name, l.Version, len(l.Texts), sample)
			if strings.Contains(e.Name, "/en") {
				l.Texts[0].Text = "Привет, мир"
				name := strings.TrimPrefix(e.Name, p.MountPoint)
				patch := unreal.WritePak(p.MountPoint, []string{name}, map[string][]byte{name: l.Bytes()})
				pp, err := unreal.OpenPak(bytes.NewReader(patch), int64(len(patch)))
				if err != nil || len(pp.Entries) != 1 {
					t.Fatalf("заплатка не читается: %v", err)
				}
				pb, err := pp.Read(pp.Entries[0])
				if err != nil {
					t.Fatal(err)
				}
				pl, err := unreal.ParseLocres(pb)
				if err != nil || pl.Texts[0].Text != "Привет, мир" || pp.Entries[0].Name != e.Name {
					t.Fatalf("в заплатке %v %q %s", err, pl.Texts[0].Text, pp.Entries[0].Name)
				}
			}
		}
	}
}

// Начало и оглавление .DAT игр TT Games: SWTR_DAT_GAME=<образ>.
func TestDatPeek(t *testing.T) {
	game := os.Getenv("SWTR_DAT_GAME")
	if game == "" {
		t.Skip("SWTR_DAT_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	g, err := nx.OpenGame(game, FindExtras(game), ks, t.TempDir(), t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, f := range g.RomFS.Files {
		if !strings.HasSuffix(f.Path, ".DAT") {
			continue
		}
		r := g.RomFS.Open(f)
		head := make([]byte, 32)
		r.ReadAt(head, 0)
		fmt.Printf("%s %d\n  начало % x\n", f.Path, f.Size, head)
		le := binary.LittleEndian
		off := int64(le.Uint32(head))
		if head[3]&0x80 != 0 {
			off = int64(le.Uint32(head)^0xFFFFFFFF)<<8 + 0x100
		}
		if off > 0 && off < f.Size {
			info := make([]byte, 256)
			r.ReadAt(info, off)
			fmt.Printf("  оглавление @%x: % x\n", off, info)
		}
	}
}

// Оглавления .DAT игр LEGO: SWTR_DAT_GAME=<образ>, SWTR_DAT_PAT — какие файлы показать.
func TestDatList(t *testing.T) {
	game := os.Getenv("SWTR_DAT_GAME")
	if game == "" {
		t.Skip("SWTR_DAT_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	g, err := nx.OpenGame(game, FindExtras(game), ks, t.TempDir(), t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pat := regexp.MustCompile(os.Getenv("SWTR_DAT_PAT"))
	for _, f := range g.RomFS.Files {
		if !strings.HasSuffix(f.Path, ".DAT") {
			continue
		}
		r := g.RomFS.Open(f)
		d, err := ttgames.Open(r, f.Size)
		if err != nil {
			fmt.Printf("%s: %v\n", f.Path, err)
			continue
		}
		named := d.Named()
		exts := map[string]int{}
		packed := 0
		for _, e := range d.Files {
			exts[strings.ToLower(path.Ext(strings.ReplaceAll(e.Name, "\\", "/")))]++
			if e.Packed {
				packed++
			}
		}
		fmt.Printf("%s: версия %d/%d, файлов %d, с именами %d, сжатых %d, хэш %d\n", f.Path, d.Version, d.FormatVer, len(d.Files), len(named), packed, d.CRCBits)
		shown := 0
		for _, e := range named {
			if os.Getenv("SWTR_DAT_PAT") != "" && pat.MatchString(e.Name) && shown < 40 {
				head := make([]byte, 12)
				if os.Getenv("SWTR_DAT_LONG") != "" {
					head = make([]byte, 300)
				}
				r.ReadAt(head, e.Offset)
				fmt.Printf("   %s %d/%d %v %q\n", e.Name, e.ZSize, e.Size, e.Packed, head)
				if out := os.Getenv("SWTR_DAT_OUT"); out != "" {
					b, err := d.Read(r, e)
					if err != nil {
						t.Fatal(err)
					}
					dst := filepath.Join(out, filepath.Base(f.Path), strings.ReplaceAll(e.Name, "\\", "/"))
					os.MkdirAll(filepath.Dir(dst), 0o755)
					os.WriteFile(dst, b, 0o644)
				}
				if os.Getenv("SWTR_DAT_RC4") != "" && string(head[:4]) == "ZIPX" {
					zs := binary.LittleEndian.Uint32(head[4:])
					body := head[12:]
					for _, key := range [][]byte{head[4:8], head[8:12], []byte(strconv.Itoa(int(zs))), []byte(fmt.Sprintf("%08x", zs))} {
						c, _ := rc4.NewCipher(key)
						out := make([]byte, len(body))
						c.XORKeyStream(out, body)
						fmt.Printf("      ключ %q: %q\n", key, out[:80])
					}
				}
				shown++
			}
		}
		if os.Getenv("SWTR_DAT_EXT") != "" {
			fmt.Println("  ", exts)
		}
	}
}

// Озвучка в архивах LEGO: SWTR_LEGO_GAME=<образ>. Реплики английские, читаются; две озвучиваются тоном,
// архив пересобирается, и в нём реплики нужной длины.
func TestLEGODub(t *testing.T) {
	game := os.Getenv("SWTR_LEGO_GAME")
	if game == "" {
		t.Skip("SWTR_LEGO_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	work := t.TempDir()
	s, err := Analyze(game, FindExtras(game), ks, work, t.Logf, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SelectLocale(s.DefaultLocale("en"))
	var idx []int
	langs := map[string]int{}
	for i, l := range s.Audio {
		if _, name, ok := splitDATSound(l.File); ok {
			if _, tag, ok := bankTag(name); ok {
				langs[tag]++
			}
			if strings.Contains(name, "/vo/") && len(idx) < 2 {
				idx = append(idx, i)
			}
		}
	}
	t.Logf("реплик %d, языки %v", len(s.Audio), langs)
	if len(idx) < 2 || langs["FRE"] > 0 {
		t.Fatalf("реплик %d, французских %d", len(idx), langs["FRE"])
	}
	wav, err := s.AudioWav(context.Background(), s.Audio[idx[0]].File)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(wav)
	p, err := audio.ReadWAV(b)
	if err != nil || p.Seconds() < 0.3 {
		t.Fatalf("реплика не расшифровалась: %v", err)
	}
	for k, i := range idx {
		tone := &audio.PCM{Rate: 48000, Channels: [][]int16{make([]int16, 48000)}}
		for j := range tone.Channels[0] {
			tone.Channels[0][j] = int16(9000 * math.Sin(2*math.Pi*float64(300+100*k)*float64(j)/48000))
		}
		dub := filepath.Join(work, "dub", s.audioBase(s.Audio[i].File)+".wav")
		if err := writeFile(dub, audio.WAV(tone)); err != nil {
			t.Fatal(err)
		}
		s.Audio[i].Dub, s.Audio[i].Selected = dub, true
	}
	res, err := s.Build(BuildOptions{To: "ru", Audio: true}, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dubs != 2 || len(res.Warnings) > 0 {
		t.Fatalf("%v: озвучек %d, %v", err, res.Dubs, res.Warnings)
	}
	for _, i := range idx {
		dat, name, _ := splitDATSound(s.Audio[i].File)
		f, err := os.Open(s.ModPath(dat))
		if err != nil {
			t.Fatal(err)
		}
		st, _ := f.Stat()
		d, err := ttgames.Open(f, st.Size())
		if err != nil {
			t.Fatal(err)
		}
		data, err := d.Read(f, d.Files[d.Index(strings.ReplaceAll(name, "/", `\`))])
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err := audio.Decode(data)
		if err != nil || math.Abs(got.Seconds()-1) > 0.01 {
			t.Fatalf("%s: %v, %.2f с", name, err, got.Seconds())
		}
	}
	t.Logf("файлы %v", res.Files)
}

// Озвучка в бандлах Unity: SWTR_BUNDLE_GAME=<образ> (Mighty Pups). Звуки — только английского бандла,
// два озвучиваются тоном, бандл пересобирается, и в нём звук нужной длины.
func TestBundleDub(t *testing.T) {
	game := os.Getenv("SWTR_BUNDLE_GAME")
	if game == "" {
		t.Skip("SWTR_BUNDLE_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	work := t.TempDir()
	s, err := Analyze(game, FindExtras(game), ks, work, t.Logf, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SelectLocale(s.DefaultLocale("en"))
	var idx []int
	langs := map[string]int{}
	for i, l := range s.Audio {
		if outer, _, ok := strings.Cut(l.File, archSep); ok && strings.HasSuffix(outer, ".bundle") {
			langs[bundleLang(outer)]++
			if bundleLang(outer) == "en" && len(idx) < 2 {
				idx = append(idx, i)
			}
		}
	}
	t.Logf("звуков %d, по языкам бандлов %v", len(s.Audio), langs)
	if len(idx) < 2 || langs["ru"] > 0 {
		t.Fatalf("английских %d, русских %d", len(idx), langs["ru"])
	}
	wav, err := s.AudioWav(context.Background(), s.Audio[idx[0]].File)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(wav)
	p, err := audio.ReadWAV(b)
	if err != nil || p.Seconds() < 0.2 {
		t.Fatalf("звук не расшифровался: %v", err)
	}
	t.Logf("%s: %.1f с", s.Audio[idx[0]].File, p.Seconds())
	for k, i := range idx {
		tone := &audio.PCM{Rate: 48000, Channels: [][]int16{make([]int16, 48000)}}
		for j := range tone.Channels[0] {
			tone.Channels[0][j] = int16(9000 * math.Sin(2*math.Pi*float64(300+100*k)*float64(j)/48000))
		}
		dub := filepath.Join(work, "dub", s.audioBase(s.Audio[i].File)+".wav")
		if err := writeFile(dub, audio.WAV(tone)); err != nil {
			t.Fatal(err)
		}
		s.Audio[i].Dub, s.Audio[i].Selected = dub, true
	}
	res, err := s.Build(BuildOptions{To: "ru", Audio: true}, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dubs != 2 || len(res.Warnings) > 0 {
		t.Fatalf("%v: озвучек %d, %v", err, res.Dubs, res.Warnings)
	}
	for _, i := range idx {
		bank, name, _ := splitBank(s.Audio[i].File)
		outer, node, _ := strings.Cut(bank, archSep)
		data, err := os.ReadFile(s.ModPath(outer))
		if err != nil {
			t.Fatal(err)
		}
		bn, err := unity.OpenBundle(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("собранный бандл не читается: %v", err)
		}
		var nd []byte
		for _, n := range bn.Nodes {
			if n.Path == node {
				nd, _ = bn.ReadNode(bytes.NewReader(data), n)
			}
		}
		fb, err := audio.ParseFMOD(bytes.NewReader(nd), int64(len(nd)))
		if err != nil {
			t.Fatal(err)
		}
		k, ok := fb.Find(name)
		if !ok {
			t.Fatalf("нет %s", name)
		}
		one, _ := fb.Sample(bytes.NewReader(nd), k)
		got, err := audio.Decode(one)
		if err != nil || math.Abs(got.Seconds()-1) > 0.02 {
			t.Fatalf("%s: %v %.2f с", name, err, got.Seconds())
		}
		// AudioClip указывает на новый FSB5: по его смещению — подпись FSB5
		for _, n := range bn.Nodes {
			if n.Path != strings.TrimSuffix(node, ".resource") {
				continue
			}
			cab, _ := bn.ReadNode(bytes.NewReader(data), n)
			refs, err := unity.AudioClipRefs(cab, path.Base(node))
			if err != nil || len(refs) == 0 {
				t.Fatalf("AudioClip не найдены: %v", err)
			}
			for _, ref := range refs {
				if ref[0]+ref[1] > int64(len(nd)) || string(nd[ref[0]:ref[0]+4]) != "FSB5" {
					t.Fatalf("AudioClip указывает мимо FSB5: %v", ref)
				}
				fsb, err := audio.ParseFMOD(bytes.NewReader(nd[ref[0]:ref[0]+ref[1]]), ref[1])
				if err != nil || len(fsb.Samples) != 1 {
					t.Fatalf("по ссылке AudioClip %v не FSB5 целиком: %v", ref, err)
				}
			}
			t.Logf("AudioClip: %d, все указывают на целые FSB5", len(refs))
		}
	}
	t.Logf("файлы %v", res.Files)
}

// Озвучка в данных звука Unity рядом с ассетами: SWTR_RES_GAME=<образ> (PAW Patrol On a Roll).
func TestResourceDub(t *testing.T) {
	game := os.Getenv("SWTR_RES_GAME")
	if game == "" {
		t.Skip("SWTR_RES_GAME не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	work := t.TempDir()
	s, err := Analyze(game, FindExtras(game), ks, work, t.Logf, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SelectLocale(s.DefaultLocale("en"))
	var idx []int
	for i, l := range s.Audio {
		if bank, _, ok := splitBank(l.File); ok && strings.HasSuffix(bank, ".resource") && !strings.Contains(bank, archSep) && len(idx) < 2 {
			idx = append(idx, i)
		}
	}
	if len(idx) < 2 {
		t.Fatalf("звуков в .resource: %d", len(idx))
	}
	for k, i := range idx {
		tone := &audio.PCM{Rate: 48000, Channels: [][]int16{make([]int16, 96000)}}
		for j := range tone.Channels[0] {
			tone.Channels[0][j] = int16(9000 * math.Sin(2*math.Pi*float64(300+100*k)*float64(j)/48000))
		}
		dub := filepath.Join(work, "dub", s.audioBase(s.Audio[i].File)+".wav")
		if err := writeFile(dub, audio.WAV(tone)); err != nil {
			t.Fatal(err)
		}
		s.Audio[i].Dub, s.Audio[i].Selected = dub, true
	}
	res, err := s.Build(BuildOptions{To: "ru", Audio: true}, func(string, float64, float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dubs != 2 || len(res.Warnings) > 0 {
		t.Fatalf("озвучек %d, %v", res.Dubs, res.Warnings)
	}
	bank, _, _ := splitBank(s.Audio[idx[0]].File)
	nd, _ := os.ReadFile(s.ModPath(bank))
	assets, err := os.ReadFile(s.ModPath(strings.TrimSuffix(bank, ".resource") + ".assets"))
	if err == nil {
		refs, _ := unity.AudioClipRefs(assets, path.Base(bank))
		for _, ref := range refs {
			if ref[0]+ref[1] > int64(len(nd)) || string(nd[ref[0]:ref[0]+4]) != "FSB5" {
				t.Fatalf("AudioClip указывает мимо FSB5: %v", ref)
			}
		}
		t.Logf("AudioClip: %d", len(refs))
	}
	t.Logf("файлы %v", res.Files)
}

// Все сжатые Oodle файлы .pak распаковываются в свой размер: SWTR_PAK_GAME=<образ>, SWTR_PAK_ALL=1.
func TestPakDecodeAll(t *testing.T) {
	game := os.Getenv("SWTR_PAK_GAME")
	if game == "" || os.Getenv("SWTR_PAK_ALL") == "" {
		t.Skip("SWTR_PAK_GAME или SWTR_PAK_ALL не задан")
	}
	home, _ := os.UserHomeDir()
	ks, _ := keys.Load(filepath.Join(home, ".switch", "prod.keys"))
	g, err := nx.OpenGame(game, FindExtras(game), ks, t.TempDir(), t.Logf, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, f := range g.RomFS.Files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".pak") {
			continue
		}
		p, err := unreal.OpenPak(g.RomFS.Open(f), f.Size)
		if err != nil {
			t.Fatal(err)
		}
		ok, bad := 0, 0
		var raw int64
		for _, e := range p.Entries {
			if e.Compression == 0 {
				continue
			}
			b, err := p.Read(e)
			if err == nil && int64(len(b)) != e.RawSize {
				err = fmt.Errorf("%d байт вместо %d", len(b), e.RawSize)
			}
			if err != nil {
				bad++
				if bad <= 20 {
					t.Errorf("%s: %v", e.Name, err)
				}
				continue
			}
			ok++
			raw += e.RawSize
		}
		t.Logf("%s: распаковано %d (%d МБ), ошибок %d", f.Path, ok, raw>>20, bad)
	}
}
