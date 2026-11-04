package nx

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"switch-translator/internal/keys"
)

// Новые NCA шифруются «стандартно»: ключ секций лежит в самой NCA (key area), зашифрованный
// key_area_key_application из prod.keys, поэтому тикет не нужен. Подписи заголовка Nintendo
// подделать нельзя — на консоли такие NCA ставятся только с патчами подписи (sigpatches).

// NCASection — секция новой NCA.
type NCASection struct {
	Header []byte // FS-заголовок 0x200; шифрование и счётчик проставит WriteNCA
	Size   int64
	Data   func(w io.Writer) error // пишет ровно Size байт открытых данных
	BKTR   bool                    // секция обновления-разницы: тип шифрования AesCtrEx, данные патча в заголовке свои
}

// NCAInfo — поля заголовка.
type NCAInfo struct {
	FirstSection int64 // где начинается первая секция; 0 — ncaFirstSection (у маленьких Meta NCA Nintendo — 0xC00)
	Type         ContentType
	ProgramID    uint64
	ContentIndex uint32
	SDK          uint32
	KeyRev       int // ревизия мастер-ключа
	SigKeyGen    byte
}

// Content — записанная NCA: что нужно для CNMT и имени файла.
type Content struct {
	Hash [32]byte
	Size int64
	Type ContentType
}

// ID — идентификатор контента: первые 16 байт SHA-256 файла, он же имя «<id>.nca».
func (c Content) ID() [16]byte {
	var id [16]byte
	copy(id[:], c.Hash[:16])
	return id
}

func (c Content) Name() string { return fmt.Sprintf("%x.nca", c.ID()) }

const ncaFirstSection = 0x4000 // как у больших NCA: заголовок NCZ (0x4000) не задевает данных

// NCALayout — где окажутся секции и размер файла (до записи). first — начало первой секции,
// 0 — ncaFirstSection.
func NCALayout(secs []NCASection, first int64) (offs []int64, size int64) {
	pos := first
	if pos == 0 {
		pos = ncaFirstSection
	}
	for _, s := range secs {
		offs = append(offs, pos)
		pos = align(pos+s.Size, 0x200)
	}
	return offs, pos
}

// WriteNCA шифрует и пишет NCA.
func WriteNCA(w io.Writer, info NCAInfo, secs []NCASection, ks *keys.Keyset) (Content, error) {
	if len(secs) == 0 || len(secs) > 4 {
		return Content{}, fmt.Errorf("в NCA должно быть от 1 до 4 секций")
	}
	kak, ok := ks.KeyAreaKeys[0][info.KeyRev]
	if !ok {
		return Content{}, fmt.Errorf("в prod.keys нет key_area_key_application_%02x", info.KeyRev)
	}
	var area [0x40]byte
	if _, err := rand.Read(area[:]); err != nil {
		return Content{}, err
	}
	key := append([]byte(nil), area[0x20:0x30]...)
	block, _ := aes.NewCipher(key)

	offs, size := NCALayout(secs, info.FirstSection)
	hdr := make([]byte, ncaHeaderSize)
	le := binary.LittleEndian
	copy(hdr[0x200:], "NCA3")
	hdr[0x205] = byte(info.Type)
	switch {
	case info.KeyRev == 0:
	case info.KeyRev == 1:
		hdr[0x206] = 2
	default:
		hdr[0x206] = 2
		hdr[0x220] = byte(info.KeyRev + 1)
	}
	le.PutUint64(hdr[0x208:], uint64(size))
	le.PutUint64(hdr[0x210:], info.ProgramID)
	le.PutUint32(hdr[0x218:], info.ContentIndex)
	le.PutUint32(hdr[0x21C:], info.SDK)
	hdr[0x221] = info.SigKeyGen
	copy(hdr[0x300:], ecbEncrypt(kak, area[:]))

	ctrs := make([][8]byte, len(secs))
	for i, s := range secs {
		fh := make([]byte, 0x200)
		copy(fh, s.Header)
		if s.BKTR {
			fh[4] = encBKTR
		} else {
			fh[4] = encCTR
			clear(fh[0x100:0x140]) // данные патча (BKTR) — не наши
		}
		le.PutUint32(fh[0x140:], 0)
		le.PutUint32(fh[0x144:], uint32(i+1))
		for j := range 8 {
			ctrs[i][j] = fh[0x140+7-j]
		}
		copy(hdr[0x400+i*0x200:], fh)
		sum := sha256.Sum256(fh)
		copy(hdr[0x280+i*0x20:], sum[:])
		e := hdr[0x240+i*0x10:]
		le.PutUint32(e[0:], uint32(offs[i]/0x200))
		le.PutUint32(e[4:], uint32(align(offs[i]+s.Size, 0x200)/0x200))
		le.PutUint32(e[8:], 1)
	}
	newXTS(ks.HeaderKey).crypt(hdr, 0, 0x200, false)

	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(w, h)}
	if _, err := cw.Write(hdr); err != nil {
		return Content{}, err
	}
	for i, s := range secs {
		if err := cw.pad(offs[i]); err != nil {
			return Content{}, err
		}
		enc := &ctrWriter{w: cw, stream: cipher.NewCTR(block, ctrIV(ctrs[i], offs[i]))}
		if err := s.Data(enc); err != nil {
			return Content{}, err
		}
		if enc.n != s.Size {
			return Content{}, fmt.Errorf("секция %d: записано %d байт вместо %d", i, enc.n, s.Size)
		}
		// хвост до 0x200 тоже шифруется — как нули
		if tail := align(offs[i]+s.Size, 0x200) - offs[i] - s.Size; tail > 0 {
			if _, err := enc.Write(make([]byte, tail)); err != nil {
				return Content{}, err
			}
		}
	}
	if err := cw.pad(size); err != nil {
		return Content{}, err
	}
	c := Content{Size: size, Type: info.Type}
	copy(c.Hash[:], h.Sum(nil))
	return c, nil
}

type ctrWriter struct {
	w      io.Writer
	stream cipher.Stream
	n      int64
	buf    []byte
}

func (c *ctrWriter) Write(p []byte) (int, error) {
	if cap(c.buf) < len(p) {
		c.buf = make([]byte, len(p))
	}
	b := c.buf[:len(p)]
	c.stream.XORKeyStream(b, p)
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// RomFSSection собирает RomFS из files во временный файл tmp (он должен быть пустым)
// и возвращает секцию NCA с деревом хэшей IVFC.
func RomFSSection(files []BuildFile, tmp io.ReadWriteSeeker) (NCASection, error) {
	img, err := LayoutRomFS(files)
	if err != nil {
		return NCASection{}, err
	}
	h := newIVFCHasher(tmp)
	if _, err := img.WriteTo(h); err != nil {
		return NCASection{}, err
	}
	tree := h.finish()
	return NCASection{
		Header: tree.fsHeader(),
		Size:   tree.hashSize() + tree.dataSize,
		Data: func(w io.Writer) error {
			for _, l := range tree.levels {
				if _, err := w.Write(l); err != nil {
					return err
				}
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			_, err := io.Copy(w, io.LimitReader(tmp, tree.dataSize))
			return err
		},
	}, nil
}

// VerifyIVFC проверяет дерево хэшей секции RomFS готовой NCA — самопроверка после сборки.
func (n *NCA) VerifyIVFC() error {
	s, err := n.romfsSection()
	if err != nil {
		return err
	}
	r, err := n.SectionReader(*s)
	if err != nil {
		return err
	}
	off, size, err := ivfcData(s)
	if err != nil {
		return err
	}
	h := newIVFCHasher(io.Discard)
	if _, err := io.Copy(h, io.NewSectionReader(r, off, size)); err != nil {
		return err
	}
	tree := h.finish()
	if !bytes.Equal(tree.fsHeader()[0x8:0xE8], s.Header[0x8:0xE8]) {
		return fmt.Errorf("хэши RomFS не сходятся с заголовком")
	}
	pos := int64(0)
	for i, l := range tree.levels {
		got := make([]byte, len(l))
		if _, err := r.ReadAt(got, pos); err != nil && err != io.EOF {
			return err
		}
		if !bytes.Equal(got, l) {
			return fmt.Errorf("уровень хэшей %d RomFS не сходится", i+1)
		}
		pos += int64(len(l))
	}
	return nil
}
