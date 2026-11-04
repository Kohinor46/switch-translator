package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"switch-translator/internal/nx"
)

// Сборка готового образа: базовая игра как есть + своё обновление (код из официального обновления,
// вся RomFS с переводом) + DLC (переведённые пересобраны, остальные как есть) — одним NSP или NSZ.
// Ставится обычным установщиком (DBI, Tinfoil, Awoo); на консоли нужны патчи подписи (sigpatches).

// ImageOptions — что собрать.
type ImageOptions struct {
	BuildOptions
	NSZ bool // сжать в NSZ (меньше, но не все установщики его понимают)
}

// ImageResult — что получилось.
type ImageResult struct {
	Mod      *BuildResult
	Path     string
	Size     int64
	Version  uint32   // версия нашего обновления
	Contents []string // что внутри, для показа
	Built    []string // имена NCA, собранных программой (остальное скопировано из исходных образов как есть)
}

// versionStep — на сколько наше обновление новее официального: так система не заменит его тем же.
const versionStep = 0x10000

// BuildImage собирает мод (перевод, озвучка, шрифты), а затем образ с ним.
func (s *Session) BuildImage(opts ImageOptions, progress Progress) (*ImageResult, error) {
	if s.keys == nil {
		return nil, fmt.Errorf("нет ключей")
	}
	mod, err := s.Build(opts.BuildOptions, progress)
	if err != nil {
		return nil, err
	}
	res := &ImageResult{Mod: mod}
	over := map[string]string{} // путь в общей RomFS → файл мода
	for _, f := range mod.Files {
		over[f] = s.ModPath(f)
	}

	tmp, err := os.MkdirTemp(filepath.Join(s.WorkDir, ".tmp"), "image-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	g := s.Game
	lang := strings.ToUpper(opts.To)
	var out []nx.PFS0File

	// 1. базовая игра — как есть
	baseFiles, err := s.copyPack(g.Base, opts.NSZ)
	if err != nil {
		return nil, err
	}
	out = append(out, baseFiles...)
	res.Contents = append(res.Contents, fmt.Sprintf("игра %016X v%d — без изменений", g.Base.TitleID, g.Base.Version))

	// 2. своё обновление
	src := g.Base
	if g.Update != nil {
		src = g.Update
	}
	res.Version = max(g.Version, g.Base.Version)/versionStep*versionStep + versionStep
	var mainFiles []nx.BuildFile
	for _, f := range g.RomFS.Files {
		if _, _, dlc := nx.SplitDLC(f.Path); !dlc {
			mainFiles = append(mainFiles, s.buildFile(f, f.Path, over))
		}
	}
	// новые файлы мода, которых в игре нет (заплатка .pak для игр на Unreal)
	inRom := map[string]bool{}
	for _, f := range g.RomFS.Files {
		inRom[f.Path] = true
	}
	for _, f := range mod.Files {
		if _, _, dlc := nx.SplitDLC(f); dlc || inRom[f] {
			continue
		}
		m := s.ModPath(f)
		st, err := os.Stat(m)
		if err != nil {
			return nil, err
		}
		mainFiles = append(mainFiles, nx.BuildFile{Path: f, Size: st.Size(), Open: func() (io.Reader, error) { return os.Open(m) }})
	}
	progress("Сборка RomFS игры", 0, 1)
	program, err := s.programNCA(tmp, src, mainFiles, progress)
	if err != nil {
		return nil, fmt.Errorf("Program NCA: %w", err)
	}
	if err := s.checkProgram(program); err != nil {
		return nil, fmt.Errorf("собранное обновление не пройдёт проверку консоли: %w", err)
	}
	control, err := s.controlNCA(tmp, src, lang)
	if err != nil {
		return nil, fmt.Errorf("Control NCA: %w", err)
	}
	patch := s.patchCNMT(res.Version, program.c, control.c)
	meta, err := s.metaNCA(tmp, patch, src)
	if err != nil {
		return nil, err
	}
	for _, n := range []*builtNCA{program, control, meta} {
		f, err := s.packNCA(tmp, n, opts.NSZ, progress)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
		res.Built = append(res.Built, f.Name)
	}
	res.Contents = append(res.Contents, fmt.Sprintf("обновление с переводом %016X v%d", patch.TitleID, res.Version))

	// 3. DLC
	for _, d := range g.DLC {
		prefix := fmt.Sprintf("%s%016X/", nx.DLCPrefix, d.TitleID)
		changed := false
		var files []nx.BuildFile
		for _, f := range g.RomFS.Files {
			if inner, ok := strings.CutPrefix(f.Path, prefix); ok {
				files = append(files, s.buildFile(f, inner, over))
				if _, ok := over[f.Path]; ok {
					changed = true
				}
			}
		}
		if !changed || d.Meta == nil {
			fs, err := s.copyPack(d, opts.NSZ)
			if err != nil {
				return nil, err
			}
			out = append(out, fs...)
			res.Contents = append(res.Contents, fmt.Sprintf("DLC %016X v%d — без изменений", d.TitleID, d.Version))
			continue
		}
		progress(fmt.Sprintf("Сборка DLC %016X", d.TitleID), 0, 1)
		data, err := s.dataNCA(tmp, d, files)
		if err != nil {
			return nil, fmt.Errorf("DLC %016X: %w", d.TitleID, err)
		}
		cn, keep, err := s.dlcCNMT(d, data.c)
		if err != nil {
			return nil, err
		}
		dm, err := s.metaNCA(tmp, cn, d)
		if err != nil {
			return nil, err
		}
		for _, n := range []*builtNCA{data, dm} {
			f, err := s.packNCA(tmp, n, opts.NSZ, progress)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
			res.Built = append(res.Built, f.Name)
		}
		out = append(out, keep...)
		res.Contents = append(res.Contents, fmt.Sprintf("DLC %016X v%d — с переводом", d.TitleID, cn.Version))
	}

	// 4. запись
	ext := ".nsp"
	if opts.NSZ {
		ext = ".nsz"
	}
	title := g.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path))
	}
	name := fmt.Sprintf("%s [%016X][v%d][%s]%s", safeName(title), g.TitleID, res.Version, lang, ext)
	res.Path = filepath.Join(s.WorkDir, name)
	res.Size = nx.PFS0Size(out)
	part := res.Path + ".part"
	f, err := os.Create(part)
	if err != nil {
		return nil, err
	}
	err = nx.WritePFS0(f, out, func(done int64) { progress("Запись образа", float64(done), float64(res.Size)) })
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return nil, err
	}
	if err := os.Rename(part, res.Path); err != nil {
		return nil, err
	}
	s.log("Образ собран: %s (%s)", res.Path, nx.HumanSize(res.Size))
	return res, nil
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)

func safeName(s string) string {
	return strings.Join(strings.Fields(unsafeChars.ReplaceAllString(s, " ")), " ")
}

// buildFile — файл игры для новой RomFS: из мода, если он изменён, иначе из образа.
func (s *Session) buildFile(f nx.RomFile, p string, over map[string]string) nx.BuildFile {
	if m, ok := over[f.Path]; ok {
		if st, err := os.Stat(m); err == nil {
			return nx.BuildFile{Path: p, Size: st.Size(), Open: func() (io.Reader, error) { return os.Open(m) }}
		}
	}
	fs := s.Game.RomFS
	bf := nx.BuildFile{Path: p, Size: f.Size, Open: func() (io.Reader, error) { return fs.Open(f), nil }}
	bf.Base, bf.InBase = s.Game.BaseOffset(f)
	return bf
}

// builtNCA — собранная NCA во временном файле.
type builtNCA struct {
	path string
	c    nx.Content
	meta bool
}

func (s *Session) writeNCA(tmp string, info nx.NCAInfo, secs []nx.NCASection) (*builtNCA, error) {
	f, err := os.CreateTemp(tmp, "nca-")
	if err != nil {
		return nil, err
	}
	c, err := nx.WriteNCA(f, info, secs, s.keys)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return &builtNCA{path: f.Name(), c: c}, nil
}

// keyRev — ревизия ключей для новых NCA: как у исходной, если такой ключ есть в prod.keys.
func (s *Session) keyRev(want int) (int, error) {
	if _, ok := s.keys.KeyAreaKeys[0][want]; ok {
		return want, nil
	}
	return 0, fmt.Errorf("в prod.keys нет key_area_key_application_%02x — ключи старее игры, сними их с консоли заново", want)
}

// programNCA: ExeFS (код) из исходной Program NCA как есть, RomFS — новая.
func (s *Session) programNCA(tmp string, src *nx.Pack, files []nx.BuildFile, progress Progress) (*builtNCA, error) {
	var exefs *nx.Section
	for i := range src.Main.Sections {
		if src.Main.Sections[i].Index == 0 && src.Main.Sections[i].FsType == 1 {
			exefs = &src.Main.Sections[i]
		}
	}
	if exefs == nil {
		return nil, fmt.Errorf("в %s нет ExeFS", filepath.Base(src.Path))
	}
	exr, err := src.Main.SectionReader(*exefs)
	if err != nil {
		return nil, err
	}
	rf, err := os.CreateTemp(tmp, "romfs-")
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	// обновление-разница: в нём только новое и изменённое, остальное консоль берёт из игры
	// прогресс — по прочитанным файлам игры: все они читаются ради хэшей, а в обновление пишется малая часть
	read := &readCounter{label: "Сборка RomFS игры", total: totalSize(files), fn: progress}
	counted := make([]nx.BuildFile, len(files))
	for i, f := range files {
		open := f.Open
		f.Open = func() (io.Reader, error) {
			r, err := open()
			if err != nil {
				return nil, err
			}
			return io.TeeReader(r, read), nil
		}
		counted[i] = f
	}
	romfs, stored, err := nx.PatchRomFSSection(counted, rf)
	if err != nil {
		return nil, err
	}
	s.log("RomFS обновления: %s своих данных из %s — остальное берётся из игры", nx.HumanSize(stored), nx.HumanSize(totalSize(files)))
	rev, err := s.keyRev(src.KeyRev)
	if err != nil {
		return nil, err
	}
	m := src.Main
	ex := *exefs
	return s.writeNCA(tmp, nx.NCAInfo{Type: nx.ContentProgram, ProgramID: m.ProgramID, ContentIndex: m.ContentIndex, SDK: m.SDK, KeyRev: rev, SigKeyGen: m.SigKeyGen},
		[]nx.NCASection{
			{Header: ex.Header, Size: ex.Size, Data: func(w io.Writer) error {
				_, err := io.Copy(w, io.NewSectionReader(exr, 0, ex.Size))
				return err
			}},
			wrapProgress(romfs, "Шифрование RomFS", progress),
		})
}

// controlNCA — control.nacp и значки из исходной Control NCA; к версии для показа дописывается язык.
func (s *Session) controlNCA(tmp string, src *nx.Pack, lang string) (*builtNCA, error) {
	ctl := src.Control
	if ctl == nil {
		ctl = s.Game.Base.Control
	}
	if ctl == nil {
		return nil, fmt.Errorf("нет Control NCA ни в обновлении, ни в игре")
	}
	r, _, err := ctl.RomFS()
	if err != nil {
		return nil, err
	}
	fs, err := nx.OpenRomFS(r)
	if err != nil {
		return nil, err
	}
	var files []nx.BuildFile
	for _, f := range fs.Files {
		data, err := io.ReadAll(fs.Open(f))
		if err != nil {
			return nil, err
		}
		if f.Path == "control.nacp" && len(data) >= 0x3070 {
			markVersion(data[0x3060:0x3070], lang)
		}
		files = append(files, nx.BuildFile{Path: f.Path, Size: int64(len(data)), Open: func() (io.Reader, error) { return bytes.NewReader(data), nil }})
	}
	rf, err := os.CreateTemp(tmp, "control-")
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	sec, err := nx.RomFSSection(files, rf)
	if err != nil {
		return nil, err
	}
	rev, err := s.keyRev(src.KeyRev)
	if err != nil {
		return nil, err
	}
	return s.writeNCA(tmp, nx.NCAInfo{Type: nx.ContentControl, ProgramID: ctl.ProgramID, SDK: ctl.SDK, KeyRev: rev, SigKeyGen: ctl.SigKeyGen}, []nx.NCASection{sec})
}

// markVersion: «1.4.2» → «1.4.2-RU» — видно в настройках консоли, что стоит перевод.
func markVersion(field []byte, lang string) {
	v := string(bytes.TrimRight(field, "\x00"))
	mark := "-" + lang
	if strings.HasSuffix(v, mark) || len(v)+len(mark) > len(field)-1 {
		return
	}
	clear(field)
	copy(field, v+mark)
}

// patchCNMT — cnmt нашего обновления: заголовок из официального (или собранный по игре), наши NCA.
func (s *Session) patchCNMT(version uint32, program, control nx.Content) *nx.CNMT {
	g := s.Game
	var cn nx.CNMT
	if g.Update != nil && g.Update.Meta != nil {
		cn = *g.Update.Meta
	} else {
		cn = nx.CNMT{TitleID: g.TitleID | 0x800, Type: nx.MetaPatch, Ext: make([]byte, 0x18)}
		binary.LittleEndian.PutUint64(cn.Ext, g.TitleID)
		if m := g.Base.Meta; m != nil && len(m.Ext) >= 0xC {
			copy(cn.Ext[8:0xC], m.Ext[8:0xC]) // требуемая версия системы
		}
		if m := g.Base.Meta; m != nil {
			cn.ReqDLSys = m.ReqDLSys
		}
	}
	cn.Version = version
	cn.Records = []nx.CNMTRecord{program.Record(), control.Record()}
	return &cn
}

func (s *Session) metaNCA(tmp string, cn *nx.CNMT, src *nx.Pack) (*builtNCA, error) {
	rev, err := s.keyRev(src.KeyRev)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(tmp, "meta-")
	if err != nil {
		return nil, err
	}
	c, err := nx.WriteMetaNCA(f, cn, rev, src.Main.SDK, s.keys)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return &builtNCA{path: f.Name(), c: c, meta: true}, nil
}

// dataNCA — новая NCA DLC с переведёнными файлами.
func (s *Session) dataNCA(tmp string, d *nx.Pack, files []nx.BuildFile) (*builtNCA, error) {
	rf, err := os.CreateTemp(tmp, "dlc-")
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	sec, err := nx.RomFSSection(files, rf)
	if err != nil {
		return nil, err
	}
	rev, err := s.keyRev(d.KeyRev)
	if err != nil {
		return nil, err
	}
	m := d.Main
	return s.writeNCA(tmp, nx.NCAInfo{Type: m.ContentType, ProgramID: m.ProgramID, ContentIndex: m.ContentIndex, SDK: m.SDK, KeyRev: rev, SigKeyGen: m.SigKeyGen}, []nx.NCASection{sec})
}

// dlcCNMT — cnmt DLC: запись о главной NCA заменяется новой, остальное (кроме Meta) переносится как есть.
func (s *Session) dlcCNMT(d *nx.Pack, data nx.Content) (*nx.CNMT, []nx.PFS0File, error) {
	cn := *d.Meta
	cn.Version = d.Version/versionStep*versionStep + versionStep
	oldID := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(d.MainFile.Name), ".nca"), ".ncz")
	cn.Records = nil
	var keep []nx.PFS0File
	for _, r := range d.Meta.Records {
		id := fmt.Sprintf("%x", r.ID)
		switch {
		case r.Type == 0: // Meta — своя
		case id == oldID:
			nr := data.Record()
			nr.Type, nr.Idx = r.Type, r.Idx
			cn.Records = append(cn.Records, nr)
		default:
			cn.Records = append(cn.Records, r)
			for _, f := range d.Files {
				if strings.HasPrefix(strings.ToLower(f.Name), id) {
					keep = append(keep, s.pfsFile(d, f))
				}
			}
		}
	}
	// тикеты для перенесённых NCA с titlekey
	if len(keep) > 0 {
		for _, f := range d.Files {
			if e := strings.ToLower(path.Ext(f.Name)); e == ".tik" || e == ".cert" {
				keep = append(keep, s.pfsFile(d, f))
			}
		}
	}
	return &cn, keep, nil
}

// copyPack — все файлы пакета как есть. Для NSP сжатые NCZ распаковываются обратно в NCA.
func (s *Session) copyPack(p *nx.Pack, nsz bool) ([]nx.PFS0File, error) {
	var out []nx.PFS0File
	for _, f := range p.Files {
		lower := strings.ToLower(f.Name)
		if strings.HasSuffix(lower, ".xml") {
			continue // cnmt.xml установщикам не нужен
		}
		if strings.HasSuffix(lower, ".ncz") && !nsz {
			size, err := nx.NCZSize(p.PFS.Open(f), s.keys)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			f, tmp := f, filepath.Join(s.WorkDir, ".tmp")
			out = append(out, nx.PFS0File{Name: strings.TrimSuffix(f.Name, filepath.Ext(f.Name)) + ".nca", Size: size,
				Open: func() (io.Reader, error) {
					pr, pw := io.Pipe()
					go func() {
						pw.CloseWithError(nx.NCZToNCA(pw, p.PFS.Open(f), f.Size, s.keys, tmp, nil))
					}()
					return pr, nil
				}})
			continue
		}
		out = append(out, s.pfsFile(p, f))
	}
	return out, nil
}

func (s *Session) pfsFile(p *nx.Pack, f nx.PFSFile) nx.PFS0File {
	return nx.PFS0File{Name: f.Name, Size: f.Size, Open: func() (io.Reader, error) { return p.PFS.Open(f), nil }}
}

// packNCA — файл для образа: NCA как есть или, для NSZ, большая NCA сжимается в NCZ.
// Сжимаются только Program и PublicData — как у утилиты nsz: Control и Meta установщики читают
// сами (название, версия), и сжатый Control DBI не разбирает («Invalid romfs header size»).
func (s *Session) packNCA(tmp string, n *builtNCA, nsz bool, progress Progress) (nx.PFS0File, error) {
	id := fmt.Sprintf("%x", n.c.ID())
	if n.meta {
		id += ".cnmt"
	}
	file := n.path
	name := id + ".nca"
	size := n.c.Size
	if nsz && compressible(n.c.Type) && n.c.Size > 1<<20 {
		f, err := os.Open(n.path)
		if err != nil {
			return nx.PFS0File{}, err
		}
		defer f.Close()
		nca, err := nx.OpenNCA(f, s.keys, nil, false)
		if err != nil {
			return nx.PFS0File{}, err
		}
		z, err := os.CreateTemp(tmp, "ncz-")
		if err != nil {
			return nx.PFS0File{}, err
		}
		err = nx.WriteNCZ(z, f, n.c.Size, nca, func(done, total int64) { progress("Сжатие в NCZ", float64(done), float64(total)) })
		if cerr := z.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nx.PFS0File{}, err
		}
		st, err := os.Stat(z.Name())
		if err != nil {
			return nx.PFS0File{}, err
		}
		file, name, size = z.Name(), id+".ncz", st.Size()
	}
	return nx.PFS0File{Name: name, Size: size, Open: func() (io.Reader, error) { return os.Open(file) }}, nil
}

// checkProgram открывает собранную Program NCA поверх игры так же, как консоль при запуске:
// таблицы обновления проверяются, RomFS читается.
func (s *Session) checkProgram(n *builtNCA) error {
	f, err := os.Open(n.path)
	if err != nil {
		return err
	}
	defer f.Close()
	nca, err := nx.OpenNCA(f, s.keys, nil, false)
	if err != nil {
		return err
	}
	r, _, err := nca.PatchedRomFS(s.Game.Base.Main)
	if err != nil {
		return err
	}
	_, err = nx.OpenRomFS(r)
	return err
}

func compressible(t nx.ContentType) bool {
	return t == nx.ContentProgram || t == nx.ContentPublicData
}

func totalSize(files []nx.BuildFile) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// readCounter считает прочитанное и показывает прогресс.
type readCounter struct {
	label string
	done  int64
	total int64
	fn    Progress
}

func (c *readCounter) Write(b []byte) (int, error) {
	c.done += int64(len(b))
	if c.done%(16<<20) < int64(len(b)) {
		c.fn(c.label, float64(c.done), float64(c.total))
	}
	return len(b), nil
}

// progressFile — временный файл RomFS, который показывает, сколько записано.
type progressFile struct {
	*os.File
	label string
	done  int64
	total int64
	fn    Progress
}

func (p *progressFile) Write(b []byte) (int, error) {
	n, err := p.File.Write(b)
	p.done += int64(n)
	if p.done%(16<<20) < int64(n) {
		p.fn(p.label, float64(p.done), float64(p.total))
	}
	return n, err
}

func wrapProgress(sec nx.NCASection, label string, fn Progress) nx.NCASection {
	data := sec.Data
	sec.Data = func(w io.Writer) error {
		return data(&progressWriter{w: w, label: label, total: sec.Size, fn: fn})
	}
	return sec
}

type progressWriter struct {
	w     io.Writer
	label string
	done  int64
	total int64
	fn    Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.done%(16<<20) < int64(n) {
		p.fn(p.label, float64(p.done), float64(p.total))
	}
	return n, err
}

// VerifyImage проверяет NCA, которые программа собрала сама: имя каждой совпадает с её SHA-256.
// Скопированные из исходных образов файлы не проверяются: в переупакованных образах (NSC_Builder и т. п.)
// имена NCA уже не совпадают с содержимым, и это не ошибка — консоль сверяет хэши по cnmt, а не по имени.
func (s *Session) VerifyImage(img *ImageResult, progress Progress) error {
	f, err := os.Open(img.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	pfs, err := nx.OpenPFS0(f)
	if err != nil {
		return err
	}
	built := map[string]bool{}
	for _, n := range img.Built {
		built[n] = true
	}
	checked := 0
	for _, pf := range pfs.Files {
		if !built[pf.Name] {
			continue
		}
		progress("Проверка образа", float64(checked), float64(len(built)))
		checked++
		lower := strings.ToLower(pf.Name)
		h := sha256.New()
		if strings.HasSuffix(lower, ".ncz") {
			if err := nx.NCZToNCA(h, pfs.Open(pf), pf.Size, s.keys, filepath.Join(s.WorkDir, ".tmp"), nil); err != nil {
				return fmt.Errorf("%s: %w", pf.Name, err)
			}
		} else if _, err := io.Copy(h, pfs.Open(pf)); err != nil {
			return err
		}
		id := strings.SplitN(lower, ".", 2)[0]
		if got := fmt.Sprintf("%x", h.Sum(nil)[:16]); got != id {
			return fmt.Errorf("%s повреждён при записи: хэш %s", pf.Name, got)
		}
	}
	if checked != len(built) {
		return fmt.Errorf("в образе нет %d собранных NCA", len(built)-checked)
	}
	return nil
}
