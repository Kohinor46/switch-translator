package nx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// PFS0 — контейнер, которым являются NSP/NSZ.
type PFS0 struct {
	r     io.ReaderAt
	Files []PFSFile
}

type PFSFile struct {
	Name   string
	Offset int64 // абсолютное смещение в контейнере
	Size   int64
}

func OpenPFS0(r io.ReaderAt) (*PFS0, error) {
	hdr := make([]byte, 0x10)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, err
	}
	if string(hdr[:4]) != "PFS0" {
		return nil, fmt.Errorf("это не NSP/NSZ: нет сигнатуры PFS0")
	}
	n := int(binary.LittleEndian.Uint32(hdr[4:]))
	strSize := int(binary.LittleEndian.Uint32(hdr[8:]))
	if n > 100000 || strSize > 1<<24 {
		return nil, fmt.Errorf("повреждённый заголовок PFS0")
	}
	meta := make([]byte, n*0x18+strSize)
	if _, err := r.ReadAt(meta, 0x10); err != nil {
		return nil, err
	}
	strs := meta[n*0x18:]
	dataStart := int64(0x10 + len(meta))

	p := &PFS0{r: r}
	for i := 0; i < n; i++ {
		e := meta[i*0x18:]
		nameOff := int(binary.LittleEndian.Uint32(e[16:]))
		if nameOff >= len(strs) {
			return nil, fmt.Errorf("повреждённая таблица имён PFS0")
		}
		name := strs[nameOff:]
		if z := bytes.IndexByte(name, 0); z >= 0 {
			name = name[:z]
		}
		p.Files = append(p.Files, PFSFile{
			Name:   string(name),
			Offset: dataStart + int64(binary.LittleEndian.Uint64(e[0:])),
			Size:   int64(binary.LittleEndian.Uint64(e[8:])),
		})
	}
	return p, nil
}

func (p *PFS0) Open(f PFSFile) *io.SectionReader {
	return io.NewSectionReader(p.r, f.Offset, f.Size)
}
