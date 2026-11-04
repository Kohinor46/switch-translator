package nx

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"switch-translator/internal/keys"
)

// Собираем NCA с RomFS и Meta NCA с cnmt на случайных ключах и читаем их своим же разборщиком.
func TestWriteNCARoundTrip(t *testing.T) {
	fx := newFixture(t)
	want := map[string][]byte{
		"Talk.msbt":              randBytes(1234),
		"Sound/bgm.bfstm":        randBytes(100000),
		"Sound/se/hit.bfwav":     randBytes(17),
		"LocalizedData/EUen/a.b": randBytes(0x4000),
	}
	var files []BuildFile
	for p, d := range want {
		files = append(files, BuildFile{Path: p, Size: int64(len(d)), Open: func() (io.Reader, error) { return bytes.NewReader(d), nil }})
	}
	tmp, err := os.CreateTemp(t.TempDir(), "romfs")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	sec, err := RomFSSection(files, tmp)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c, err := WriteNCA(&out, NCAInfo{Type: ContentProgram, ProgramID: 0x0100ABCD00000000}, []NCASection{sec}, fx.ks)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(out.Bytes()); sum != c.Hash || int64(out.Len()) != c.Size {
		t.Fatal("хэш или размер NCA не совпадает")
	}
	n, err := OpenNCA(bytes.NewReader(out.Bytes()), fx.ks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.VerifyIVFC(); err != nil {
		t.Fatal(err)
	}
	r, _, err := n.RomFS()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := OpenRomFS(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Files) != len(want) {
		t.Fatalf("файлов %d", len(fs.Files))
	}
	for _, f := range fs.Files {
		got, _ := io.ReadAll(fs.Open(f))
		if !bytes.Equal(got, want[f.Path]) {
			t.Errorf("%s не совпал", f.Path)
		}
	}

	cn := &CNMT{TitleID: 0x0100ABCD00000000, Version: 0x10000, Type: MetaApplication, Ext: make([]byte, 0x10), Records: []CNMTRecord{c.Record()}}
	var meta bytes.Buffer
	if _, err := WriteMetaNCA(&meta, cn, 0, 0, fx.ks); err != nil {
		t.Fatal(err)
	}
	mn, err := OpenNCA(bytes.NewReader(meta.Bytes()), fx.ks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadCNMT(mn)
	if err != nil {
		t.Fatal(err)
	}
	if back.TitleID != cn.TitleID || back.Version != cn.Version || len(back.Records) != 1 || back.Records[0] != c.Record() {
		t.Fatalf("cnmt не совпал: %+v", back)
	}
	// раскладка как у Nintendo — иначе DBI не разбирает cnmt («Invalid romfs header size»):
	// секция с 0xC00, PFS0 вплотную за таблицей хэшей
	ms := mn.Sections[0]
	h := ms.Header[0x8:]
	if ms.Offset != 0xC00 || binary.LittleEndian.Uint64(h[0x38:]) != binary.LittleEndian.Uint64(h[0x30:]) {
		t.Errorf("раскладка Meta NCA: секция с %#x, таблица %d байт, PFS0 с %#x", ms.Offset,
			binary.LittleEndian.Uint64(h[0x30:]), binary.LittleEndian.Uint64(h[0x38:]))
	}

	// NSP из двух NCA читается
	nsp := filepath.Join(t.TempDir(), "x.nsp")
	f, _ := os.Create(nsp)
	err = WritePFS0(f, []PFS0File{
		{Name: c.Name(), Size: c.Size, Open: func() (io.Reader, error) { return bytes.NewReader(out.Bytes()), nil }},
		{Name: "meta.cnmt.nca", Size: int64(meta.Len()), Open: func() (io.Reader, error) { return bytes.NewReader(meta.Bytes()), nil }},
	}, nil)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	g, err := OpenGame(nsp, nil, fx.ks, t.TempDir(), func(string, ...any) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if len(g.RomFS.Files) != len(want) {
		t.Fatalf("в NSP %d файлов", len(g.RomFS.Files))
	}
}

// NCZ из своей NCA и обратно — байт в байт.
func TestNCZRoundTrip(t *testing.T) {
	fx := newFixture(t)
	data := randBytes(300000)
	files := []BuildFile{{Path: "a/b.bin", Size: int64(len(data)), Open: func() (io.Reader, error) { return bytes.NewReader(data), nil }}}
	tmp, _ := os.CreateTemp(t.TempDir(), "romfs")
	defer tmp.Close()
	sec, err := RomFSSection(files, tmp)
	if err != nil {
		t.Fatal(err)
	}
	var nca bytes.Buffer
	if _, err := WriteNCA(&nca, NCAInfo{Type: ContentProgram}, []NCASection{sec}, fx.ks); err != nil {
		t.Fatal(err)
	}
	n, _ := OpenNCA(bytes.NewReader(nca.Bytes()), fx.ks, nil, false)
	var ncz bytes.Buffer
	if err := WriteNCZ(&ncz, bytes.NewReader(nca.Bytes()), int64(nca.Len()), n, nil); err != nil {
		t.Fatal(err)
	}
	var back bytes.Buffer
	if err := NCZToNCA(&back, bytes.NewReader(ncz.Bytes()), int64(ncz.Len()), fx.ks, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Bytes(), nca.Bytes()) {
		a, b := back.Bytes(), nca.Bytes()
		d := 0
		for d < min(len(a), len(b)) && a[d] == b[d] {
			d++
		}
		t.Fatalf("NCA после NCZ не совпала: размер %x/%x, отличие с %x, секции %+v", len(a), len(b), d, n.Sections[0].Offset)
	}
}

// Настоящий NCZ: SWTR_NCZ=<файл .nsz> — восстановленная NCA должна совпасть с именем (её SHA-256).
func TestNCZReal(t *testing.T) {
	p := os.Getenv("SWTR_NCZ")
	if p == "" {
		t.Skip("SWTR_NCZ не задан")
	}
	ks, err := keys.Load(os.ExpandEnv("$HOME/.switch/prod.keys"))
	if err != nil {
		t.Skip(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	pfs, err := OpenPFS0(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, pf := range pfs.Files {
		if filepath.Ext(pf.Name) != ".ncz" {
			continue
		}
		h := sha256.New()
		if err := NCZToNCA(h, pfs.Open(pf), pf.Size, ks, t.TempDir(), nil); err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", h.Sum(nil)[:16])
		if got+".ncz" != pf.Name {
			t.Errorf("%s: получилось %s", pf.Name, got)
		}
	}
}

// Обновление-разница: файлы игры берутся из базовой NCA, в обновлении — только новое и изменённое.
// Читаем своим же разборщиком BKTR (он проверен на настоящих обновлениях) и сверяем каждый файл.
func TestPatchRomFS(t *testing.T) {
	fx := newFixture(t)
	big := randBytes(300000)
	baseFiles := map[string][]byte{"a/keep.bin": big, "a/change.msbt": randBytes(5000), "z/keep2.bin": randBytes(70000)}
	var bf []BuildFile
	for p, d := range baseFiles {
		bf = append(bf, BuildFile{Path: p, Size: int64(len(d)), Open: func() (io.Reader, error) { return bytes.NewReader(d), nil }})
	}
	tmp, _ := os.CreateTemp(t.TempDir(), "b")
	defer tmp.Close()
	sec, err := RomFSSection(bf, tmp)
	if err != nil {
		t.Fatal(err)
	}
	var baseNCA bytes.Buffer
	if _, err := WriteNCA(&baseNCA, NCAInfo{Type: ContentProgram, ProgramID: 0x0100ABCD00000000}, []NCASection{sec}, fx.ks); err != nil {
		t.Fatal(err)
	}
	base, err := OpenNCA(bytes.NewReader(baseNCA.Bytes()), fx.ks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	bsec, _ := base.romfsSection()
	dataOff, _, _ := ivfcData(bsec)
	r, _, _ := base.RomFS()
	bfs, _ := OpenRomFS(r)
	where := map[string]int64{}
	for _, f := range bfs.Files {
		where[f.Path] = dataOff + f.Offset
	}

	want := map[string][]byte{"a/keep.bin": big, "a/change.msbt": randBytes(5100), "z/keep2.bin": baseFiles["z/keep2.bin"], "new/ru.txt": []byte("привет")}
	var pf []BuildFile
	for p, d := range want {
		f := BuildFile{Path: p, Size: int64(len(d)), Open: func() (io.Reader, error) { return bytes.NewReader(d), nil }}
		if off, ok := where[p]; ok && bytes.Equal(baseFiles[p], d) {
			f.InBase, f.Base = true, off
		}
		pf = append(pf, f)
	}
	ptmp, _ := os.CreateTemp(t.TempDir(), "p")
	defer ptmp.Close()
	psec, stored, err := PatchRomFSSection(pf, ptmp)
	if err != nil {
		t.Fatal(err)
	}
	var patchNCA bytes.Buffer
	if _, err := WriteNCA(&patchNCA, NCAInfo{Type: ContentProgram, ProgramID: 0x0100ABCD00000000}, []NCASection{psec}, fx.ks); err != nil {
		t.Fatal(err)
	}
	if stored > 100000 {
		t.Errorf("в обновление попало %d байт — файлы игры должны браться из неё", stored)
	}
	patch, err := OpenNCA(bytes.NewReader(patchNCA.Bytes()), fx.ks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	pr, psize, err := patch.PatchedRomFS(base)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := OpenRomFS(pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Files) != len(want) {
		t.Fatalf("файлов %d", len(fs.Files))
	}
	for _, f := range fs.Files {
		got, _ := io.ReadAll(fs.Open(f))
		if !bytes.Equal(got, want[f.Path]) {
			t.Errorf("%s не совпал", f.Path)
		}
	}
	// хэши IVFC в заголовке сходятся с тем, что консоль прочитает
	h := newIVFCHasher(io.Discard)
	io.Copy(h, io.NewSectionReader(pr, 0, psize))
	ps, _ := patch.romfsSection()
	if !bytes.Equal(h.finish().fsHeader()[0x8:0xE8], ps.Header[0x8:0xE8]) {
		t.Error("хэши IVFC не сходятся")
	}
	// NCZ из такой NCA разворачивается обратно байт в байт
	var ncz, back bytes.Buffer
	if err := WriteNCZ(&ncz, bytes.NewReader(patchNCA.Bytes()), int64(patchNCA.Len()), patch, nil); err != nil {
		t.Fatal(err)
	}
	if err := NCZToNCA(&back, bytes.NewReader(ncz.Bytes()), int64(ncz.Len()), fx.ks, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Bytes(), patchNCA.Bytes()) {
		t.Error("NCZ обновления не совпал")
	}
	t.Logf("игра %d байт, обновление %d байт (своих данных %d)", baseNCA.Len(), patchNCA.Len(), stored)
}
