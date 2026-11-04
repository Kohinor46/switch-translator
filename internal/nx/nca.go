package nx

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"

	"switch-translator/internal/keys"
)

const ncaHeaderSize = 0xC00

type ContentType byte

const (
	ContentProgram ContentType = iota
	ContentMeta
	ContentControl
	ContentManual
	ContentData
	ContentPublicData
)

func (c ContentType) String() string {
	names := []string{"Program", "Meta", "Control", "Manual", "Data", "PublicData"}
	if int(c) < len(names) {
		return names[c]
	}
	return fmt.Sprintf("Unknown(%d)", c)
}

const (
	fsRomFS = 0
	fsPFS0  = 1

	encNone = 1
	encXTS  = 2
	encCTR  = 3
	encBKTR = 4
)

type Section struct {
	Index    int
	Offset   int64 // абсолютное смещение в NCA
	Size     int64
	FsType   byte
	HashType byte
	EncType  byte
	Ctr      [8]byte
	Header   []byte // FS-заголовок, 0x200 байт
}

type NCA struct {
	r         io.ReaderAt
	plainBody bool // NCZ: тело уже расшифровано

	ContentType  ContentType
	ProgramID    uint64
	ContentSize  int64
	RightsID     [16]byte
	MasterKeyRev int
	ContentIndex uint32
	SDK          uint32
	SigKeyGen    byte
	Sections     []Section

	ctrKey []byte
	keyErr error
}

// DecryptHeader расшифровывает первые 0xC00 байт NCA.
func DecryptHeader(raw []byte, ks *keys.Keyset) ([]byte, error) {
	if len(raw) < ncaHeaderSize {
		return nil, fmt.Errorf("NCA короче заголовка")
	}
	hdr := append([]byte(nil), raw[:ncaHeaderSize]...)
	newXTS(ks.HeaderKey).crypt(hdr, 0, 0x200, true)
	switch string(hdr[0x200:0x204]) {
	case "NCA3":
		return hdr, nil
	case "NCA2", "NCA0":
		return nil, fmt.Errorf("старый формат %s не поддерживается", hdr[0x200:0x204])
	default:
		return nil, fmt.Errorf("не удалось расшифровать заголовок NCA — неверный header_key?")
	}
}

// OpenNCA разбирает NCA. tickets — тикеты из того же NSP (rights id → тикет).
func OpenNCA(r io.ReaderAt, ks *keys.Keyset, tickets map[[16]byte]*Ticket, plainBody bool) (*NCA, error) {
	raw := make([]byte, ncaHeaderSize)
	if _, err := r.ReadAt(raw, 0); err != nil {
		return nil, err
	}
	hdr, err := DecryptHeader(raw, ks)
	if err != nil {
		return nil, err
	}

	n := &NCA{
		r:           r,
		plainBody:   plainBody,
		ContentType: ContentType(hdr[0x205]),
		ContentSize: int64(binary.LittleEndian.Uint64(hdr[0x208:])),
		ProgramID:   binary.LittleEndian.Uint64(hdr[0x210:]),
	}
	copy(n.RightsID[:], hdr[0x230:0x240])
	n.ContentIndex = binary.LittleEndian.Uint32(hdr[0x218:])
	n.SDK = binary.LittleEndian.Uint32(hdr[0x21C:])
	n.SigKeyGen = hdr[0x221]

	rev := int(max(hdr[0x206], hdr[0x220]))
	if rev > 0 {
		rev--
	}
	n.MasterKeyRev = rev

	for i := 0; i < 4; i++ {
		e := hdr[0x240+i*0x10:]
		start := int64(binary.LittleEndian.Uint32(e[0:])) * 0x200
		end := int64(binary.LittleEndian.Uint32(e[4:])) * 0x200
		if end <= start {
			continue
		}
		fh := hdr[0x400+i*0x200 : 0x600+i*0x200]
		s := Section{
			Index: i, Offset: start, Size: end - start,
			FsType: fh[2], HashType: fh[3], EncType: fh[4],
			Header: fh,
		}
		for j := 0; j < 8; j++ {
			s.Ctr[j] = fh[0x140+7-j]
		}
		n.Sections = append(n.Sections, s)
	}

	if !plainBody {
		n.ctrKey, n.keyErr = n.deriveKey(hdr, ks, tickets)
	}
	return n, nil
}

func (n *NCA) HasRightsID() bool { return n.RightsID != [16]byte{} }

func (n *NCA) deriveKey(hdr []byte, ks *keys.Keyset, tickets map[[16]byte]*Ticket) ([]byte, error) {
	rev := n.MasterKeyRev
	if n.HasRightsID() {
		var enc []byte
		if t, ok := tickets[n.RightsID]; ok && !t.Personalized {
			enc = t.EncTitleKey[:]
		} else if k, ok := ks.TitleKeys[n.RightsID]; ok {
			enc = k
		} else {
			return nil, fmt.Errorf("нет title key для rights id %x: добавь title.keys или NSP с тикетом", n.RightsID)
		}
		kek, ok := ks.Titlekeks[rev]
		if !ok {
			return nil, fmt.Errorf("в prod.keys нет titlekek_%02x — ключи старее игры, обнови их с консоли", rev)
		}
		return ecbDecrypt(kek, enc), nil
	}

	idx := int(hdr[0x207])
	if idx > 2 {
		return nil, fmt.Errorf("неизвестный индекс key area: %d", idx)
	}
	kak, ok := ks.KeyAreaKeys[idx][rev]
	if !ok {
		return nil, fmt.Errorf("в prod.keys нет %s%02x — ключи старее игры, обнови их с консоли", []string{"key_area_key_application_", "key_area_key_ocean_", "key_area_key_system_"}[idx], rev)
	}
	area := ecbDecrypt(kak, hdr[0x300:0x340])
	return area[0x20:0x30], nil
}

// SectionReader отдаёт расшифрованные данные секции (смещения — от начала секции).
func (n *NCA) SectionReader(s Section) (io.ReaderAt, error) {
	if n.plainBody || s.EncType == encNone {
		return io.NewSectionReader(n.r, s.Offset, s.Size), nil
	}
	switch s.EncType {
	case encCTR:
		if n.keyErr != nil {
			return nil, n.keyErr
		}
		block, _ := aes.NewCipher(n.ctrKey)
		return &ctrReader{r: n.r, base: s.Offset, size: s.Size, block: block, ctr: s.Ctr}, nil
	case encBKTR:
		return nil, fmt.Errorf("это обновление игры: выбери базовую игру, а обновление укажи в поле «Обновление»")
	default:
		return nil, fmt.Errorf("тип шифрования секции %d не поддерживается", s.EncType)
	}
}

// RomFS находит секцию RomFS и возвращает читатель её данных (без IVFC-хэшей).
func (n *NCA) RomFS() (io.ReaderAt, int64, error) {
	for _, s := range n.Sections {
		if s.FsType != fsRomFS {
			continue
		}
		sec, err := n.SectionReader(s)
		if err != nil {
			return nil, 0, err
		}
		off, size, err := ivfcData(&s)
		if err != nil {
			return nil, 0, err
		}
		return io.NewSectionReader(sec, off, size), size, nil
	}
	return nil, 0, fmt.Errorf("в NCA нет RomFS")
}

type ctrReader struct {
	r     io.ReaderAt
	base  int64
	size  int64
	block cipher.Block
	ctr   [8]byte
}

func (c *ctrReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= c.size {
		return 0, io.EOF
	}
	n := len(p)
	var tailErr error
	if off+int64(n) > c.size {
		n = int(c.size - off)
		tailErr = io.EOF
	}
	abs := c.base + off
	start := abs &^ 15
	pad := int(abs - start)
	buf := make([]byte, pad+n)
	got, err := c.r.ReadAt(buf, start)
	if got < len(buf) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	cipher.NewCTR(c.block, ctrIV(c.ctr, start)).XORKeyStream(buf, buf)
	copy(p, buf[pad:])
	return n, tailErr
}
