package nx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"switch-translator/internal/keys"
)

// CNMT — список содержимого «тайтла» (игры, обновления, DLC): что ставить и с какими хэшами.

const (
	MetaApplication  = 0x80
	MetaPatch        = 0x81
	MetaAddOnContent = 0x82
)

// cnmt-типы содержимого отличаются от типов в заголовке NCA.
var cnmtContentType = map[ContentType]byte{
	ContentMeta: 0, ContentProgram: 1, ContentData: 2, ContentControl: 3, ContentManual: 4, ContentPublicData: 2,
}

// CNMT — разобранный cnmt.
type CNMT struct {
	TitleID  uint64
	Version  uint32
	Type     byte
	Ext      []byte // расширенный заголовок как есть
	Attr     byte
	ReqDLSys uint32
	Records  []CNMTRecord
}

type CNMTRecord struct {
	Hash [32]byte
	ID   [16]byte
	Size int64
	Type byte
	Idx  byte
}

func ParseCNMT(b []byte) (*CNMT, error) {
	if len(b) < 0x20 {
		return nil, fmt.Errorf("cnmt обрезан")
	}
	le := binary.LittleEndian
	c := &CNMT{TitleID: le.Uint64(b), Version: le.Uint32(b[8:]), Type: b[12], Attr: b[0x17], ReqDLSys: le.Uint32(b[0x18:])}
	ext := int(le.Uint16(b[0xE:]))
	n := int(le.Uint16(b[0x10:]))
	if 0x20+ext+n*0x38 > len(b) {
		return nil, fmt.Errorf("cnmt обрезан")
	}
	c.Ext = append([]byte(nil), b[0x20:0x20+ext]...)
	for i := range n {
		e := b[0x20+ext+i*0x38:]
		var r CNMTRecord
		copy(r.Hash[:], e)
		copy(r.ID[:], e[0x20:])
		var sz [8]byte
		copy(sz[:], e[0x30:0x36])
		r.Size = int64(le.Uint64(sz[:]))
		r.Type, r.Idx = e[0x36], e[0x37]
		c.Records = append(c.Records, r)
	}
	return c, nil
}

// Bytes собирает cnmt; у обновления расширенные данные (история патчей) не пишутся.
func (c *CNMT) Bytes() []byte {
	le := binary.LittleEndian
	ext := append([]byte(nil), c.Ext...)
	if c.Type == MetaPatch && len(ext) >= 0x10 {
		le.PutUint32(ext[0xC:], 0)
	}
	b := make([]byte, 0x20, 0x20+len(ext)+len(c.Records)*0x38+0x20)
	le.PutUint64(b, c.TitleID)
	le.PutUint32(b[8:], c.Version)
	b[12] = c.Type
	le.PutUint16(b[0xE:], uint16(len(ext)))
	le.PutUint16(b[0x10:], uint16(len(c.Records)))
	b[0x17] = c.Attr
	le.PutUint32(b[0x18:], c.ReqDLSys)
	b = append(b, ext...)
	for _, r := range c.Records {
		e := make([]byte, 0x38)
		copy(e, r.Hash[:])
		copy(e[0x20:], r.ID[:])
		var sz [8]byte
		le.PutUint64(sz[:], uint64(r.Size))
		copy(e[0x30:], sz[:6])
		e[0x36], e[0x37] = r.Type, r.Idx
		b = append(b, e...)
	}
	return append(b, make([]byte, 0x20)...) // дайджест
}

// Record — запись о собранной NCA.
func (c Content) Record() CNMTRecord {
	return CNMTRecord{Hash: c.Hash, ID: c.ID(), Size: c.Size, Type: cnmtContentType[c.Type]}
}

func (c *CNMT) FileName() string {
	kind := map[byte]string{MetaApplication: "Application", MetaPatch: "Patch", MetaAddOnContent: "AddOnContent"}[c.Type]
	return fmt.Sprintf("%s_%016x.cnmt", kind, c.TitleID)
}

// PFS0File — файл для упаковки в PFS0 (NSP или секцию NCA).
type PFS0File struct {
	Name string
	Size int64
	Open func() (io.Reader, error)
}

// pfs0Header — заголовок PFS0; данные идут сразу за ним.
func pfs0Header(files []PFS0File) []byte {
	var names []byte
	offs := make([]uint32, len(files))
	for i, f := range files {
		offs[i] = uint32(len(names))
		names = append(names, f.Name...)
		names = append(names, 0)
	}
	// заголовок вместе с именами — кратно 0x20
	for (0x10+len(files)*0x18+len(names))%0x20 != 0 {
		names = append(names, 0)
	}
	le := binary.LittleEndian
	h := make([]byte, 0x10, 0x10+len(files)*0x18+len(names))
	copy(h, "PFS0")
	le.PutUint32(h[4:], uint32(len(files)))
	le.PutUint32(h[8:], uint32(len(names)))
	var pos int64
	for i, f := range files {
		e := make([]byte, 0x18)
		le.PutUint64(e[0:], uint64(pos))
		le.PutUint64(e[8:], uint64(f.Size))
		le.PutUint32(e[16:], offs[i])
		h = append(h, e...)
		pos += f.Size
	}
	return append(h, names...)
}

// PFS0Size — размер готового PFS0.
func PFS0Size(files []PFS0File) int64 {
	n := int64(len(pfs0Header(files)))
	for _, f := range files {
		n += f.Size
	}
	return n
}

// WritePFS0 пишет PFS0 (NSP/NSZ) потоком. progress получает число записанных байт.
func WritePFS0(w io.Writer, files []PFS0File, progress func(done int64)) error {
	cw := &countWriter{w: w}
	if _, err := cw.Write(pfs0Header(files)); err != nil {
		return err
	}
	for _, f := range files {
		r, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		n, err := copyProgress(cw, io.LimitReader(r, f.Size), func() {
			if progress != nil {
				progress(cw.n)
			}
		})
		if c, ok := r.(io.Closer); ok {
			c.Close()
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		if n != f.Size {
			return fmt.Errorf("%s: записано %d байт вместо %d", f.Name, n, f.Size)
		}
	}
	return nil
}

func copyProgress(w io.Writer, r io.Reader, tick func()) (int64, error) {
	buf := make([]byte, 4<<20)
	var n int64
	for {
		k, err := r.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			tick()
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// WriteMetaNCA пишет Meta NCA с cnmt внутри.
func WriteMetaNCA(w io.Writer, c *CNMT, keyRev int, sdk uint32, ks *keys.Keyset) (Content, error) {
	data := c.Bytes()
	files := []PFS0File{{Name: c.FileName(), Size: int64(len(data)), Open: func() (io.Reader, error) { return bytes.NewReader(data), nil }}}
	var pfs bytes.Buffer
	if err := WritePFS0(&pfs, files, nil); err != nil {
		return Content{}, err
	}
	fh, table, dataOff := sha256Layers(pfs.Bytes())
	sec := make([]byte, dataOff+int64(pfs.Len()))
	copy(sec, table)
	copy(sec[dataOff:], pfs.Bytes())
	// раскладка как у Meta NCA Nintendo: секция сразу за заголовком (0xC00), PFS0 вплотную за таблицей хэшей —
	// DBI других не понимает («Invalid romfs header size»)
	return WriteNCA(w, NCAInfo{FirstSection: 0xC00, Type: ContentMeta, ProgramID: c.TitleID, SDK: sdk, KeyRev: keyRev},
		[]NCASection{{Header: fh, Size: int64(len(sec)), Data: func(w io.Writer) error { _, err := w.Write(sec); return err }}}, ks)
}

// ReadCNMT достаёт cnmt из Meta NCA.
func ReadCNMT(n *NCA) (*CNMT, error) {
	for _, s := range n.Sections {
		if s.FsType != fsPFS0 {
			continue
		}
		r, err := n.SectionReader(s)
		if err != nil {
			return nil, err
		}
		h := s.Header[0x8:]
		layers := int(binary.LittleEndian.Uint32(h[0x24:]))
		if layers < 1 || layers > 5 {
			return nil, fmt.Errorf("неожиданный FS-заголовок Meta NCA")
		}
		reg := h[0x28+(layers-1)*0x10:]
		pfs, err := OpenPFS0(io.NewSectionReader(r, int64(binary.LittleEndian.Uint64(reg)), int64(binary.LittleEndian.Uint64(reg[8:]))))
		if err != nil {
			return nil, err
		}
		for _, f := range pfs.Files {
			if strings.HasSuffix(f.Name, ".cnmt") {
				b := make([]byte, f.Size)
				if _, err := pfs.Open(f).ReadAt(b, 0); err != nil && err != io.EOF {
					return nil, err
				}
				return ParseCNMT(b)
			}
		}
	}
	return nil, fmt.Errorf("в Meta NCA нет cnmt")
}
