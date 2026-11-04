package nx

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
)

// RomFS собирается так же, как у Nintendo: заголовок, данные файлов (выравнивание 0x10),
// затем хэш-таблицы и записи каталогов и файлов. Консоль ищет пути через хэш-таблицы,
// поэтому они должны быть точно такими, как ждёт FS.

// BuildFile — файл будущей RomFS.
type BuildFile struct {
	Path string // без ведущего «/»
	Size int64
	Open func() (io.Reader, error)
	// InBase — те же байты уже лежат в секции RomFS базовой игры по смещению Base:
	// в обновлении (BKTR) такой файл не хранится, а берётся из игры.
	InBase bool
	Base   int64
}

type romDir struct {
	name    string
	parent  *romDir
	dirs    []*romDir
	files   []*romFileEntry
	off     uint32
	sibling *romDir
}

type romFileEntry struct {
	name    string
	parent  *romDir
	src     BuildFile
	off     uint32
	dataOff int64
	sibling *romFileEntry
}

const romfsHeaderSize = 0x50
const romfsDataStart = 0x200

func romfsHash(parent uint32, name string) uint32 {
	h := parent ^ 123456789
	for i := 0; i < len(name); i++ {
		h = h>>5 | h<<27
		h ^= uint32(name[i])
	}
	return h
}

func romfsBuckets(n int) uint32 {
	switch {
	case n < 3:
		return 3
	case n < 19:
		return uint32(n | 1)
	}
	c := uint32(n)
	for c%2 == 0 || c%3 == 0 || c%5 == 0 || c%7 == 0 || c%11 == 0 || c%13 == 0 || c%17 == 0 {
		c++
	}
	return c
}

func align(n, a int64) int64 { return (n + a - 1) / a * a }

// RomFSImage — раскладка будущей RomFS: размер известен до записи.
type RomFSImage struct {
	Size  int64
	meta  []byte // таблицы после данных
	files []*romFileEntry
	data  int64 // где кончаются данные
}

// LayoutRomFS раскладывает файлы. Пути сравниваются побайтно, как у Nintendo.
func LayoutRomFS(files []BuildFile) (*RomFSImage, error) {
	root := &romDir{}
	dirs := []*romDir{root}
	byPath := map[string]*romDir{"": root}
	var getDir func(p string) *romDir
	getDir = func(p string) *romDir {
		if d, ok := byPath[p]; ok {
			return d
		}
		parent, name := "", p
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			parent, name = p[:i], p[i+1:]
		}
		pd := getDir(parent)
		d := &romDir{name: name, parent: pd}
		pd.dirs = append(pd.dirs, d)
		byPath[p] = d
		dirs = append(dirs, d)
		return d
	}
	var all []*romFileEntry
	seen := map[string]bool{}
	for _, f := range files {
		p := strings.Trim(f.Path, "/")
		if p == "" || seen[p] {
			return nil, fmt.Errorf("RomFS: пустой или повторяющийся путь %q", f.Path)
		}
		seen[p] = true
		dir, name := "", p
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir, name = p[:i], p[i+1:]
		}
		d := getDir(dir)
		f.Path = p
		e := &romFileEntry{name: name, parent: d, src: f}
		d.files = append(d.files, e)
		all = append(all, e)
	}

	// внутри каталога — по имени
	for _, d := range dirs {
		slices.SortFunc(d.dirs, func(a, b *romDir) int { return strings.Compare(a.name, b.name) })
		slices.SortFunc(d.files, func(a, b *romFileEntry) int { return strings.Compare(a.name, b.name) })
		for i := 0; i+1 < len(d.dirs); i++ {
			d.dirs[i].sibling = d.dirs[i+1]
		}
		for i := 0; i+1 < len(d.files); i++ {
			d.files[i].sibling = d.files[i+1]
		}
	}
	// каталоги — в глубину, как у Nintendo
	var order []*romDir
	var walk func(d *romDir)
	walk = func(d *romDir) {
		order = append(order, d)
		for _, c := range d.dirs {
			walk(c)
		}
	}
	walk(root)
	var dirMetaSize, fileMetaSize uint32
	for _, d := range order {
		d.off = dirMetaSize
		dirMetaSize += 0x18 + uint32(align(int64(len(d.name)), 4))
	}
	// записи файлов и данные — по полному пути, как у Nintendo
	img := &RomFSImage{files: all}
	slices.SortFunc(img.files, func(a, b *romFileEntry) int { return strings.Compare(a.src.Path, b.src.Path) })
	var dataPos int64
	for _, f := range img.files {
		f.off = fileMetaSize
		fileMetaSize += 0x20 + uint32(align(int64(len(f.name)), 4))
		dataPos = align(dataPos, 0x10)
		f.dataOff = dataPos
		dataPos += f.src.Size
	}

	none := uint32(romfsNone)
	dirBuckets, fileBuckets := romfsBuckets(len(order)), romfsBuckets(len(all))
	dirHash := make([]uint32, dirBuckets)
	fileHash := make([]uint32, fileBuckets)
	for i := range dirHash {
		dirHash[i] = none
	}
	for i := range fileHash {
		fileHash[i] = none
	}
	le := binary.LittleEndian
	dirMeta := make([]byte, dirMetaSize)
	for _, d := range order {
		e := dirMeta[d.off:]
		parent := uint32(0)
		if d.parent != nil {
			parent = d.parent.off
		}
		h := romfsHash(parent, d.name) % dirBuckets
		le.PutUint32(e[0:], parent)
		le.PutUint32(e[4:], none)
		if d.sibling != nil {
			le.PutUint32(e[4:], d.sibling.off)
		}
		le.PutUint32(e[8:], none)
		if len(d.dirs) > 0 {
			le.PutUint32(e[8:], d.dirs[0].off)
		}
		le.PutUint32(e[12:], none)
		if len(d.files) > 0 {
			le.PutUint32(e[12:], d.files[0].off)
		}
		le.PutUint32(e[16:], dirHash[h])
		dirHash[h] = d.off
		le.PutUint32(e[20:], uint32(len(d.name)))
		copy(e[0x18:], d.name)
	}
	fileMeta := make([]byte, fileMetaSize)
	for _, f := range img.files {
		e := fileMeta[f.off:]
		h := romfsHash(f.parent.off, f.name) % fileBuckets
		le.PutUint32(e[0:], f.parent.off)
		le.PutUint32(e[4:], none)
		if f.sibling != nil {
			le.PutUint32(e[4:], f.sibling.off)
		}
		le.PutUint64(e[8:], uint64(f.dataOff))
		le.PutUint64(e[16:], uint64(f.src.Size))
		le.PutUint32(e[24:], fileHash[h])
		fileHash[h] = f.off
		le.PutUint32(e[28:], uint32(len(f.name)))
		copy(e[0x20:], f.name)
	}

	// таблицы — после данных, с выравниванием 0x10
	metaStart := align(romfsDataStart+dataPos, 0x10)
	img.data = romfsDataStart + dataPos
	var meta []byte
	tables := make([]int64, 0, 8)
	for _, t := range [][]byte{u32s(dirHash), dirMeta, u32s(fileHash), fileMeta} {
		tables = append(tables, metaStart+int64(len(meta)), int64(len(t)))
		meta = append(meta, t...)
	}
	img.meta = make([]byte, romfsHeaderSize, romfsHeaderSize+len(meta))
	le.PutUint64(img.meta[0:], romfsHeaderSize)
	for i, v := range tables {
		le.PutUint64(img.meta[8+i*8:], uint64(v))
	}
	le.PutUint64(img.meta[0x48:], romfsDataStart)
	img.meta = append(img.meta, meta...)
	img.Size = metaStart + int64(len(meta))
	return img, nil
}

func u32s(v []uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], x)
	}
	return b
}

// WriteTo пишет RomFS целиком (поток: заголовок, данные, таблицы).
func (img *RomFSImage) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	hdr := make([]byte, romfsDataStart)
	copy(hdr, img.meta[:romfsHeaderSize])
	if _, err := cw.Write(hdr); err != nil {
		return cw.n, err
	}
	for _, f := range img.files {
		if err := cw.pad(romfsDataStart + f.dataOff); err != nil {
			return cw.n, err
		}
		r, err := f.src.Open()
		if err != nil {
			return cw.n, fmt.Errorf("%s: %w", f.src.Path, err)
		}
		n, err := io.Copy(cw, io.LimitReader(r, f.src.Size))
		if c, ok := r.(io.Closer); ok {
			c.Close()
		}
		if err != nil {
			return cw.n, fmt.Errorf("%s: %w", f.src.Path, err)
		}
		if n != f.src.Size {
			return cw.n, fmt.Errorf("%s: прочитано %d байт вместо %d", f.src.Path, n, f.src.Size)
		}
	}
	if err := cw.pad(img.Size - int64(len(img.meta)-romfsHeaderSize)); err != nil {
		return cw.n, err
	}
	_, err := cw.Write(img.meta[romfsHeaderSize:])
	return cw.n, err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// pad дописывает нули до позиции to.
func (c *countWriter) pad(to int64) error {
	if to < c.n {
		return fmt.Errorf("внутренняя ошибка раскладки: %d < %d", to, c.n)
	}
	_, err := c.Write(make([]byte, to-c.n))
	return err
}

// romfsSeg — кусок собранной RomFS: из базовой игры (phys — смещение в её секции) или из обновления.
type romfsSeg struct {
	off, size int64
	base      bool
	phys      int64
}

// writeSplit пишет всю RomFS в all (для хэшей), а в patch — только то, чего нет в базовой игре.
// Возвращает раскладку кусков по порядку.
func (img *RomFSImage) writeSplit(all, patch io.Writer) ([]romfsSeg, error) {
	var segs []romfsSeg
	pos := int64(0)
	emit := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if _, err := all.Write(b); err != nil {
			return err
		}
		if _, err := patch.Write(b); err != nil {
			return err
		}
		if n := len(segs); n > 0 && !segs[n-1].base && segs[n-1].off+segs[n-1].size == pos {
			segs[n-1].size += int64(len(b))
		} else {
			segs = append(segs, romfsSeg{off: pos, size: int64(len(b))})
		}
		pos += int64(len(b))
		return nil
	}
	hdr := make([]byte, romfsDataStart)
	copy(hdr, img.meta[:romfsHeaderSize])
	if err := emit(hdr); err != nil {
		return nil, err
	}
	buf := make([]byte, 1<<20)
	for _, f := range img.files {
		if err := emit(make([]byte, romfsDataStart+f.dataOff-pos)); err != nil {
			return nil, err
		}
		r, err := f.src.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.src.Path, err)
		}
		lr := io.LimitReader(r, f.src.Size)
		var read int64
		for {
			n, rerr := lr.Read(buf)
			if n > 0 {
				read += int64(n)
				if f.src.InBase {
					// байты из игры: только для хэшей, в обновление не попадают
					if _, err := all.Write(buf[:n]); err != nil {
						return nil, err
					}
					pos += int64(n)
				} else if err := emit(buf[:n]); err != nil {
					return nil, err
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return nil, fmt.Errorf("%s: %w", f.src.Path, rerr)
			}
		}
		if c, ok := r.(io.Closer); ok {
			c.Close()
		}
		if read != f.src.Size {
			return nil, fmt.Errorf("%s: прочитано %d байт вместо %d", f.src.Path, read, f.src.Size)
		}
		if f.src.InBase && f.src.Size > 0 {
			segs = append(segs, romfsSeg{off: romfsDataStart + f.dataOff, size: f.src.Size, base: true, phys: f.src.Base})
		}
	}
	if err := emit(make([]byte, img.Size-int64(len(img.meta)-romfsHeaderSize)-pos)); err != nil {
		return nil, err
	}
	if err := emit(img.meta[romfsHeaderSize:]); err != nil {
		return nil, err
	}
	return segs, nil
}
