package nx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"switch-translator/internal/i18n"

	"switch-translator/internal/keys"
)

// DLCPrefix — файлы DLC видны в общей RomFS как «DLC/<title id>/<путь>».
const DLCPrefix = "DLC/"

// SplitDLC разбирает путь: файл DLC или игры.
func SplitDLC(p string) (tid uint64, inner string, ok bool) {
	rest, found := strings.CutPrefix(p, DLCPrefix)
	if !found {
		return 0, p, false
	}
	id, inner, found := strings.Cut(rest, "/")
	if !found {
		return 0, p, false
	}
	v, err := strconv.ParseUint(id, 16, 64)
	if err != nil {
		return 0, p, false
	}
	return v, inner, true
}

// Game — игра со всеми обновлениями и DLC: общая RomFS (игра + последнее обновление + DLC).
type Game struct {
	Path    string
	TitleID uint64
	RomFS   *RomFS

	Base   *Pack
	Update *Pack   // последнее обновление (nil — без него)
	DLC    []*Pack // по title id

	// Обновление, наложенное на базовую игру ("" — без обновления).
	UpdatePath string
	Version    uint32 // версия из обновления: v393216 и т. п.
	Title      string // название из control.nacp
	Display    string // версия так, как её показывает консоль: 1.3.0

	// откуда брать файлы игры для обновления-разницы
	baseDataOff int64           // где данные RomFS в секции базовой игры (без обновления)
	upMap       *indirectReader // таблица перемещений официального обновления
	upDataOff   int64

	closers []func()
}

// BaseOffset — где байты файла игры (не DLC) лежат в секции RomFS базовой игры.
// false — файл изменён или добавлен обновлением: его байтов в игре нет.
func (g *Game) BaseOffset(f RomFile) (int64, bool) {
	if _, _, dlc := SplitDLC(f.Path); dlc || f.Size == 0 {
		return 0, false
	}
	if g.Update == nil {
		return g.baseDataOff + f.Offset, true
	}
	if g.upMap == nil {
		return 0, false
	}
	return g.upMap.baseRange(g.upDataOff+f.Offset, f.Size)
}

func (g *Game) Close() {
	for _, c := range g.closers {
		c()
	}
	g.closers = nil
}

type Logger func(format string, args ...any)

// Pack — открытый NSP/NSZ: игра, обновление или DLC.
type Pack struct {
	Path    string
	Kind    byte   // MetaApplication, MetaPatch, MetaAddOnContent
	TitleID uint64 // из cnmt: у обновления …800, у DLC — свой
	Version uint32
	Meta    *CNMT
	Files   []PFSFile
	PFS     *PFS0
	KeyRev  int

	Main     *NCA // Program (игра, обновление) или Data (DLC)
	MainFile PFSFile
	Control  *NCA
	title    string
	display  string
	closers  []func()
}

// KindName — по-русски, для сообщений.
func (p *Pack) KindName() string {
	switch p.Kind {
	case MetaPatch:
		return "обновление"
	case MetaAddOnContent:
		return "DLC"
	}
	return "игра"
}

// openPacks открывает NSP/NSZ: cnmt, главную NCA (NCZ распаковывается) и Control.
// В одном файле может лежать несколько тайтлов — игра, обновление, DLC (так собраны образы
// «всё в одном»): тогда каждый cnmt даёт свой Pack со своими NCA.
func openPacks(path string, ks *keys.Keyset, tmpDir string, log Logger, progress func(done, total int64)) ([]*Pack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	var packs []*Pack
	fail := func(err error) ([]*Pack, error) {
		for _, p := range packs {
			p.close()
		}
		if len(packs) == 0 {
			f.Close()
		}
		return nil, err
	}

	pfs, err := OpenPFS0(f)
	if err != nil {
		return fail(err)
	}
	log("%s: %d файлов, %s", filepath.Base(path), len(pfs.Files), HumanSize(st.Size()))

	tickets := map[[16]byte]*Ticket{}
	for _, pf := range pfs.Files {
		if !strings.HasSuffix(strings.ToLower(pf.Name), ".tik") {
			continue
		}
		data := make([]byte, pf.Size)
		if _, err := pfs.Open(pf).ReadAt(data, 0); err != nil && err != io.EOF {
			return fail(err)
		}
		t, err := ParseTicket(data)
		if err != nil {
			log("Тикет %s пропущен: %v", pf.Name, err)
			continue
		}
		if t.Personalized {
			log("Тикет %s персонализированный — нужен title.keys", pf.Name)
		}
		tickets[t.RightsID] = t
	}

	// заголовки всех NCA и все cnmt
	type ncaInfo struct {
		i       int
		typ     ContentType
		ncz     bool
		keyRev  int
		metaFor *CNMT // для NCA с cnmt — что в нём
	}
	var ncas []ncaInfo
	var metas []ncaInfo
	for i, pf := range pfs.Files {
		name := strings.ToLower(pf.Name)
		isNCZ := strings.HasSuffix(name, ".ncz")
		if !isNCZ && !strings.HasSuffix(name, ".nca") {
			continue
		}
		var hdrReader io.ReaderAt = pfs.Open(pf)
		if isNCZ {
			hdrReader = NCZHeader(pfs.Open(pf))
		}
		nca, err := OpenNCA(hdrReader, ks, tickets, isNCZ)
		if err != nil {
			log("%s: %v", pf.Name, err)
			continue
		}
		info := ncaInfo{i: i, typ: nca.ContentType, ncz: isNCZ, keyRev: nca.MasterKeyRev}
		if nca.ContentType == ContentMeta && !isNCZ {
			if full, err := OpenNCA(pfs.Open(pf), ks, tickets, false); err == nil {
				if m, err := ReadCNMT(full); err == nil {
					info.metaFor = m
					metas = append(metas, info)
				}
			}
		}
		ncas = append(ncas, info)
	}

	// какие NCA к какому тайтлу: по списку в cnmt; без cnmt — всё в одном
	type group struct {
		meta  *CNMT
		metaI int
		ids   map[string]bool
	}
	var groups []group
	for _, m := range metas {
		g := group{meta: m.metaFor, metaI: m.i, ids: map[string]bool{fileID(pfs.Files[m.i].Name): true}}
		for _, r := range m.metaFor.Records {
			g.ids[fmt.Sprintf("%x", r.ID)] = true
		}
		groups = append(groups, g)
	}
	if len(groups) == 0 {
		groups = []group{{metaI: -1}}
	}
	// сначала игра, потом обновления и DLC
	sort.SliceStable(groups, func(a, b int) bool {
		return groups[a].meta != nil && groups[b].meta != nil && groups[a].meta.Type < groups[b].meta.Type
	})
	multi := len(groups) > 1

	for gi, gr := range groups {
		mine := func(name string) bool { return gr.ids == nil || gr.ids[fileID(name)] }
		p := &Pack{Path: path, PFS: pfs}
		if gi == 0 {
			p.closers = append(p.closers, func() { f.Close() })
		}
		packs = append(packs, p)
		var main, control *PFSFile
		for _, n := range ncas {
			pf := pfs.Files[n.i]
			if !mine(pf.Name) {
				continue
			}
			switch n.typ {
			case ContentProgram, ContentData, ContentPublicData:
				if main == nil || pf.Size > main.Size {
					main = &pfs.Files[n.i]
					p.KeyRev = n.keyRev
				}
			case ContentControl:
				if !n.ncz {
					control = &pfs.Files[n.i]
				}
			}
		}
		if m := gr.meta; m != nil {
			p.Meta, p.Kind, p.TitleID, p.Version = m, m.Type, m.TitleID, m.Version
		}
		// файлы этого тайтла: его NCA, их cnmt.xml, тикеты и сертификаты его title id
		for _, pf := range pfs.Files {
			name := strings.ToLower(pf.Name)
			switch {
			case !multi:
			case strings.HasSuffix(name, ".nca") || strings.HasSuffix(name, ".ncz") || strings.HasSuffix(name, ".xml"):
				if !mine(name) {
					continue
				}
			case strings.HasSuffix(name, ".tik") || strings.HasSuffix(name, ".cert"):
				if !strings.HasPrefix(name, fmt.Sprintf("%016x", p.TitleID)) {
					continue
				}
			}
			p.Files = append(p.Files, pf)
		}
		if main == nil {
			return fail(fmt.Errorf("в %s нет NCA с файлами игры", filepath.Base(path)))
		}
		p.MainFile = *main

		if control != nil {
			if nca, err := OpenNCA(pfs.Open(*control), ks, tickets, false); err == nil {
				p.Control = nca
				p.title, p.display = readNACP(nca)
			}
		}

		var ncaReader io.ReaderAt = pfs.Open(*main)
		plain := strings.HasSuffix(strings.ToLower(main.Name), ".ncz")
		if plain {
			log("Распаковываю NCZ…")
			r, closer, err := OpenNCZ(pfs.Open(*main), main.Size, tmpDir, progress)
			if err != nil {
				return fail(err)
			}
			p.closers = append(p.closers, closer)
			ncaReader = r
		}
		p.Main, err = OpenNCA(ncaReader, ks, tickets, plain)
		if err != nil {
			return fail(err)
		}
		if p.Meta == nil {
			// без cnmt: угадываем по title id
			p.TitleID = p.Main.ProgramID
			switch {
			case p.Main.ContentType != ContentProgram:
				p.Kind = MetaAddOnContent
			case p.Main.Sections != nil && hasBKTR(p.Main):
				p.Kind, p.TitleID = MetaPatch, p.TitleID|0x800
			default:
				p.Kind = MetaApplication
			}
		}
		kind := []rune(p.KindName())
		log("%s %016X v%d (%s, ревизия ключей %02x)", strings.ToUpper(string(kind[:1]))+string(kind[1:]), p.TitleID, p.Version, main.Name, p.KeyRev)
	}
	return packs, nil
}

// hasPatchOrDLC — в файле path есть обновление или DLC (значит, это образ «всё в одном»).
func hasPatchOrDLC(ps []*Pack, path string) bool {
	for _, p := range ps {
		if p.Path == path && p.Kind != MetaApplication {
			return true
		}
	}
	return false
}

// fileID — id содержимого из имени файла: «0123….nca», «….cnmt.nca», «….cnmt.xml» → «0123…».
func fileID(name string) string {
	id, _, _ := strings.Cut(strings.ToLower(filepath.Base(name)), ".")
	return id
}

func hasBKTR(n *NCA) bool {
	for _, s := range n.Sections {
		if s.EncType == encBKTR {
			return true
		}
	}
	return false
}

func (p *Pack) close() {
	for _, c := range p.closers {
		c()
	}
}

// AppTitleID — title id игры, к которой относится обновление или DLC.
func (p *Pack) AppTitleID() uint64 {
	switch p.Kind {
	case MetaPatch:
		return p.TitleID &^ 0x800
	case MetaAddOnContent:
		return (p.TitleID - 0x1000) &^ 0xFFF
	}
	return p.TitleID
}

// OpenGame открывает игру и дополнения к ней: extras — обновления и DLC в любом порядке.
// Из обновлений берётся самое новое: оно уже содержит все предыдущие.
func OpenGame(path string, extras []string, ks *keys.Keyset, tmpDir string, log Logger, progress func(done, total int64)) (*Game, error) {
	first, err := openPacks(path, ks, tmpDir, log, progress)
	if err != nil {
		return nil, err
	}
	base := first[0] // игра идёт первой, если она есть
	g := &Game{Path: path, TitleID: base.TitleID, Base: base, Title: base.title, Display: base.display, Version: base.Version}
	for _, p := range first {
		g.closers = append(g.closers, p.close)
	}
	fail := func(err error) (*Game, error) { g.Close(); return nil, err }
	if base.Kind != MetaApplication {
		return fail(fmt.Errorf("%s — это %s, а нужен образ самой игры", filepath.Base(path), base.KindName()))
	}

	// обновления и DLC: из выбранных файлов и из самого образа, если он «всё в одном»
	others := first[1:]
	for _, x := range extras {
		ps, err := openPacks(x, ks, tmpDir, log, progress)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", filepath.Base(x), err))
		}
		for _, p := range ps {
			g.closers = append(g.closers, p.close)
		}
		others = append(others, ps...)
	}
	dlc := map[uint64]*Pack{}
	for _, p := range others {
		x := p.Path
		if p.AppTitleID() != base.TitleID {
			return fail(fmt.Errorf("%s — %s от другой игры (%016X), а игра — %016X", filepath.Base(x), p.KindName(), p.AppTitleID(), base.TitleID))
		}
		switch p.Kind {
		case MetaPatch:
			if g.Update == nil || p.Version > g.Update.Version {
				g.Update = p
			}
		case MetaAddOnContent:
			if old := dlc[p.TitleID]; old == nil || p.Version > old.Version {
				dlc[p.TitleID] = p
			}
		default:
			if p.Path != path && p.TitleID == base.TitleID && len(others) > 0 && hasPatchOrDLC(others, p.Path) {
				log("%s: игра внутри пропущена — она уже открыта, взяты обновление и DLC", filepath.Base(x))
				continue
			}
			return fail(fmt.Errorf("%s — ещё один образ игры; дополнительно выбираются только обновления и DLC", filepath.Base(x)))
		}
	}
	for _, p := range dlc {
		g.DLC = append(g.DLC, p)
	}
	sort.Slice(g.DLC, func(i, j int) bool { return g.DLC[i].TitleID < g.DLC[j].TitleID })

	var parts []romPart
	if up := g.Update; up != nil {
		sec, ir, off, size, err := up.Main.patched(base.Main)
		if err != nil {
			return fail(fmt.Errorf("обновление: %w", err))
		}
		g.upMap, g.upDataOff = ir, off
		r := io.NewSectionReader(sec, off, size)
		g.UpdatePath, g.Version = up.Path, up.Version
		if up.display != "" {
			g.Display = up.display
		}
		if up.title != "" {
			g.Title = up.title
		}
		log("Обновление v%d (%s) наложено на игру", up.Version, g.Display)
		parts = append(parts, romPart{r: r, size: size})
	} else {
		r, size, err := base.Main.RomFS()
		if err != nil {
			return fail(err)
		}
		if bs, err := base.Main.romfsSection(); err == nil {
			g.baseDataOff, _, _ = ivfcData(bs)
		}
		parts = append(parts, romPart{r: r, size: size})
	}
	for _, d := range g.DLC {
		r, size, err := d.Main.RomFS()
		if err != nil {
			return fail(fmt.Errorf("DLC %016X: %w", d.TitleID, err))
		}
		parts = append(parts, romPart{r: r, size: size, prefix: fmt.Sprintf("%s%016X/", DLCPrefix, d.TitleID)})
	}
	g.RomFS, err = joinRomFS(parts)
	if err != nil {
		return fail(err)
	}
	var total int64
	for _, p := range parts {
		total += p.size
	}
	log("RomFS: %d файлов, %s (DLC: %d)", len(g.RomFS.Files), HumanSize(total), len(g.DLC))
	return g, nil
}

type romPart struct {
	r      io.ReaderAt
	size   int64
	prefix string
}

// joinRomFS склеивает несколько RomFS в одну: файлы DLC получают префикс пути.
func joinRomFS(parts []romPart) (*RomFS, error) {
	cat := &catReader{}
	out := &RomFS{r: cat}
	for _, p := range parts {
		fs, err := OpenRomFS(p.r)
		if err != nil {
			return nil, err
		}
		base := cat.size
		cat.parts = append(cat.parts, catPart{start: base, size: p.size, r: p.r})
		cat.size += p.size
		for _, f := range fs.Files {
			f.Path = p.prefix + f.Path
			f.Offset += base
			out.Files = append(out.Files, f)
		}
	}
	return out, nil
}

type catPart struct {
	start, size int64
	r           io.ReaderAt
}

// catReader — несколько ReaderAt подряд.
type catReader struct {
	parts []catPart
	size  int64
}

func (c *catReader) ReadAt(p []byte, off int64) (int, error) {
	done := 0
	for done < len(p) {
		i := sort.Search(len(c.parts), func(i int) bool { return c.parts[i].start+c.parts[i].size > off })
		if i == len(c.parts) {
			return done, io.EOF
		}
		pt := c.parts[i]
		n := int(min(int64(len(p)-done), pt.start+pt.size-off))
		got, err := pt.r.ReadAt(p[done:done+n], off-pt.start)
		done += got
		off += int64(got)
		if got < n {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return done, err
		}
	}
	return done, nil
}

// Extract копирует файлы RomFS в dir. filter == nil — все файлы.
func (g *Game) Extract(dir string, filter func(RomFile) bool, progress func(done, total int)) error {
	var list []RomFile
	for _, f := range g.RomFS.Files {
		if filter == nil || filter(f) {
			list = append(list, f)
		}
	}
	for i, f := range list {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if !strings.HasPrefix(dst, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("подозрительный путь в RomFS: %s", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, g.RomFS.Open(f))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		if progress != nil {
			progress(i+1, len(list))
		}
	}
	return nil
}

func HumanSize(n int64) string {
	f := float64(n)
	for _, u := range []string{"Б", "КБ", "МБ", "ГБ"} {
		if f < 1024 {
			return fmt.Sprintf("%.0f %s", f, i18n.T(u))
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f %s", f, i18n.T("ТБ"))
}
