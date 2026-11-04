package nx

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/klauspost/compress/zstd"

	"switch-translator/internal/keys"
)

// nczSection — запись таблицы NCZSECTN: как зашифровать кусок обратно.
type nczSection struct {
	off, size int64
	crypto    uint64
	key       [16]byte
	ctr       [16]byte
}

// WriteNCZ сжимает свою (только что собранную) NCA: первые 0x4000 байт как есть,
// дальше — расшифрованное тело в zstd. n — та же NCA, открытая с ключами.
func WriteNCZ(w io.Writer, r io.ReaderAt, size int64, n *NCA, progress func(done, total int64)) error {
	if n.keyErr != nil {
		return n.keyErr
	}
	head := make([]byte, nczHeaderSize)
	if _, err := r.ReadAt(head, 0); err != nil {
		return err
	}
	var secs []nczSection
	for _, s := range n.Sections {
		if s.Offset < nczHeaderSize {
			return fmt.Errorf("секция NCA начинается раньше 0x4000 — в NCZ так нельзя")
		}
		e := nczSection{off: s.Offset, size: s.Size, crypto: uint64(encCTR)}
		copy(e.key[:], n.ctrKey)
		copy(e.ctr[:], s.Ctr[:])
		secs = append(secs, e)
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i].off < secs[j].off })
	if _, err := w.Write(head); err != nil {
		return err
	}
	if _, err := w.Write(nczTable(secs)); err != nil {
		return err
	}
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	plain := &plainNCA{n: n, r: r}
	body := io.NewSectionReader(plain, nczHeaderSize, size-nczHeaderSize)
	if _, err := io.Copy(enc, &progressReader{r: body, total: size - nczHeaderSize, fn: progress}); err != nil {
		enc.Close()
		return err
	}
	return enc.Close()
}

func nczTable(secs []nczSection) []byte {
	b := make([]byte, 0x10, 0x10+len(secs)*0x40)
	copy(b, "NCZSECTN")
	binary.LittleEndian.PutUint64(b[8:], uint64(len(secs)))
	for _, s := range secs {
		e := make([]byte, 0x40)
		binary.LittleEndian.PutUint64(e[0:], uint64(s.off))
		binary.LittleEndian.PutUint64(e[8:], uint64(s.size))
		binary.LittleEndian.PutUint64(e[16:], s.crypto)
		copy(e[0x20:], s.key[:])
		copy(e[0x30:], s.ctr[:])
		b = append(b, e...)
	}
	return b
}

// plainNCA — NCA с расшифрованными секциями (то, что лежит в теле NCZ).
type plainNCA struct {
	n *NCA
	r io.ReaderAt
}

func (p *plainNCA) ReadAt(b []byte, off int64) (int, error) {
	done := 0
	for done < len(b) {
		pos := off + int64(done)
		var sec *Section
		next := int64(-1)
		for i := range p.n.Sections {
			s := &p.n.Sections[i]
			if s.Offset <= pos && pos < s.Offset+s.Size {
				sec = s
			} else if s.Offset > pos && (next < 0 || s.Offset < next) {
				next = s.Offset
			}
		}
		var n int
		var err error
		if sec != nil {
			k := int(min(int64(len(b)-done), sec.Offset+sec.Size-pos))
			// свои NCA шифруются одним счётчиком на секцию, в том числе секция BKTR (поколение 0)
			block, berr := aes.NewCipher(p.n.ctrKey)
			if berr != nil {
				return done, berr
			}
			sr := &ctrReader{r: p.r, base: sec.Offset, size: sec.Size, block: block, ctr: sec.Ctr}
			n, err = sr.ReadAt(b[done:done+k], pos-sec.Offset)
		} else {
			k := len(b) - done
			if next >= 0 {
				k = int(min(int64(k), next-pos))
			}
			n, err = p.r.ReadAt(b[done:done+k], pos)
		}
		done += n
		if err != nil {
			if err == io.EOF && done == len(b) {
				return done, nil
			}
			return done, err
		}
	}
	return done, nil
}

// NCZSize — размер NCA, которая получится из NCZ (по заголовку).
func NCZSize(r io.ReaderAt, ks *keys.Keyset) (int64, error) {
	raw := make([]byte, ncaHeaderSize)
	if _, err := r.ReadAt(raw, 0); err != nil {
		return 0, err
	}
	hdr, err := DecryptHeader(raw, ks)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(hdr[0x208:])), nil
}

// NCZToNCA восстанавливает исходную NCA из NCZ: тело снова шифруется по таблице NCZSECTN.
func NCZToNCA(w io.Writer, r io.ReaderAt, size int64, ks *keys.Keyset, tmpDir string, progress func(done, total int64)) error {
	ncaSize, err := NCZSize(r, ks)
	if err != nil {
		return err
	}
	hdr := make([]byte, 0x10)
	if _, err := r.ReadAt(hdr, nczHeaderSize); err != nil {
		return err
	}
	count := int(binary.LittleEndian.Uint64(hdr[8:]))
	tbl := make([]byte, count*0x40)
	if _, err := r.ReadAt(tbl, nczHeaderSize+0x10); err != nil {
		return err
	}
	var secs []nczSection
	for i := range count {
		e := tbl[i*0x40:]
		s := nczSection{off: int64(binary.LittleEndian.Uint64(e)), size: int64(binary.LittleEndian.Uint64(e[8:])), crypto: binary.LittleEndian.Uint64(e[16:])}
		copy(s.key[:], e[0x20:])
		copy(s.ctr[:], e[0x30:])
		secs = append(secs, s)
	}
	plain, closer, err := OpenNCZ(r, size, tmpDir, nil)
	if err != nil {
		return err
	}
	defer closer()
	buf := make([]byte, 1<<20)
	for pos := int64(0); pos < ncaSize; {
		k := int(min(int64(len(buf)), ncaSize-pos))
		chunk := buf[:k]
		if n, err := plain.ReadAt(chunk, pos); n < k {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("NCZ: %w", err)
		}
		for _, s := range secs {
			if s.crypto != uint64(encCTR) && s.crypto != uint64(encBKTR) {
				continue
			}
			// первые 0x4000 байт в NCZ и так зашифрованы
			lo, hi := max(pos, s.off, nczHeaderSize), min(pos+int64(k), s.off+s.size)
			if lo >= hi {
				continue
			}
			block, _ := aes.NewCipher(s.key[:])
			var c8 [8]byte
			copy(c8[:], s.ctr[:8])
			// блоки по 16 байт выровнены: секции начинаются на границе 0x200
			cipher.NewCTR(block, ctrIV(c8, lo)).XORKeyStream(chunk[lo-pos:hi-pos], chunk[lo-pos:hi-pos])
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		pos += int64(k)
		if progress != nil {
			progress(pos, ncaSize)
		}
	}
	return nil
}
