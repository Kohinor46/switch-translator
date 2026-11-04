package nx

import (
	"encoding/binary"
	"fmt"
	"io"
	"path"
)

const romfsNone = 0xFFFFFFFF

type RomFS struct {
	r     io.ReaderAt
	Files []RomFile
}

type RomFile struct {
	Path   string
	Offset int64 // от начала RomFS
	Size   int64
}

func OpenRomFS(r io.ReaderAt) (*RomFS, error) {
	h := make([]byte, 0x50)
	if _, err := r.ReadAt(h, 0); err != nil {
		return nil, err
	}
	u64 := func(o int) int64 { return int64(binary.LittleEndian.Uint64(h[o:])) }
	if u64(0) != 0x50 {
		return nil, fmt.Errorf("неверный заголовок RomFS — скорее всего, не тот ключ")
	}
	dirMeta, err := readChunk(r, u64(0x18), u64(0x20))
	if err != nil {
		return nil, err
	}
	fileMeta, err := readChunk(r, u64(0x38), u64(0x40))
	if err != nil {
		return nil, err
	}
	fs := &RomFS{r: r}
	dataOff := u64(0x48)

	var walkDir func(off uint32, prefix string, depth int) error
	walkDir = func(off uint32, prefix string, depth int) error {
		if depth > 64 || int(off)+0x18 > len(dirMeta) {
			return fmt.Errorf("повреждённая таблица каталогов RomFS")
		}
		d := dirMeta[off:]
		child := binary.LittleEndian.Uint32(d[8:])
		file := binary.LittleEndian.Uint32(d[12:])

		for file != romfsNone {
			if int(file)+0x20 > len(fileMeta) {
				return fmt.Errorf("повреждённая таблица файлов RomFS")
			}
			f := fileMeta[file:]
			nameLen := int(binary.LittleEndian.Uint32(f[0x1C:]))
			if int(file)+0x20+nameLen > len(fileMeta) {
				return fmt.Errorf("повреждённая таблица файлов RomFS")
			}
			fs.Files = append(fs.Files, RomFile{
				Path:   path.Join(prefix, string(f[0x20:0x20+nameLen])),
				Offset: dataOff + int64(binary.LittleEndian.Uint64(f[8:])),
				Size:   int64(binary.LittleEndian.Uint64(f[0x10:])),
			})
			file = binary.LittleEndian.Uint32(f[4:])
		}
		for child != romfsNone {
			if int(child)+0x18 > len(dirMeta) {
				return fmt.Errorf("повреждённая таблица каталогов RomFS")
			}
			c := dirMeta[child:]
			nameLen := int(binary.LittleEndian.Uint32(c[0x14:]))
			if int(child)+0x18+nameLen > len(dirMeta) {
				return fmt.Errorf("повреждённая таблица каталогов RomFS")
			}
			if err := walkDir(child, path.Join(prefix, string(c[0x18:0x18+nameLen])), depth+1); err != nil {
				return err
			}
			child = binary.LittleEndian.Uint32(c[4:])
		}
		return nil
	}
	if err := walkDir(0, "", 0); err != nil {
		return nil, err
	}
	return fs, nil
}

func (fs *RomFS) Open(f RomFile) *io.SectionReader {
	return io.NewSectionReader(fs.r, f.Offset, f.Size)
}

func readChunk(r io.ReaderAt, off, size int64) ([]byte, error) {
	if size < 0 || size > 1<<28 {
		return nil, fmt.Errorf("неправдоподобный размер таблицы RomFS: %d", size)
	}
	b := make([]byte, size)
	_, err := r.ReadAt(b, off)
	return b, err
}
