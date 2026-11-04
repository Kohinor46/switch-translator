package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

// Wwise: банк .bnk может хранить звуки WEM прямо внутри — оглавление в чанке DIDX (номер, смещение, размер),
// сами WEM подряд в чанке DATA. Звук называется своим номером; расшифровывает WEM vgmstream.

// BNKSound — звук WEM внутри банка Wwise.
type BNKSound struct {
	Name      string
	off, size int64
}

// Size — сколько байт занимает звук.
func (s BNKSound) Size() int64 { return s.size }

type BNK struct {
	Sounds []BNKSound
}

// ParseBNK читает оглавление звуков банка; банк без встроенных звуков (Init.bnk, только события) — ошибка.
func ParseBNK(r io.ReaderAt, size int64) (*BNK, error) {
	var didx []byte
	dataAt := int64(-1)
	h := make([]byte, 8)
	for off := int64(0); off+8 <= size; {
		if _, err := r.ReadAt(h, off); err != nil {
			return nil, err
		}
		tag, n := string(h[:4]), int64(binary.LittleEndian.Uint32(h[4:]))
		if off+8+n > size {
			break
		}
		switch tag {
		case "DIDX":
			if n > 1<<24 {
				return nil, fmt.Errorf("Wwise: DIDX слишком большой")
			}
			didx = make([]byte, n)
			if _, err := r.ReadAt(didx, off+8); err != nil {
				return nil, err
			}
		case "DATA":
			dataAt = off + 8
		}
		off += 8 + n
	}
	if didx == nil || dataAt < 0 {
		return nil, fmt.Errorf("в банке Wwise нет встроенных звуков")
	}
	b := &BNK{}
	for i := 0; i+12 <= len(didx); i += 12 {
		id := binary.LittleEndian.Uint32(didx[i:])
		o, n := int64(binary.LittleEndian.Uint32(didx[i+4:])), int64(binary.LittleEndian.Uint32(didx[i+8:]))
		if n == 0 || dataAt+o+n > size {
			continue
		}
		b.Sounds = append(b.Sounds, BNKSound{Name: strconv.FormatUint(uint64(id), 10), off: dataAt + o, size: n})
	}
	if len(b.Sounds) == 0 {
		return nil, fmt.Errorf("в банке Wwise нет встроенных звуков")
	}
	return b, nil
}

// Find — номер звука по имени.
func (b *BNK) Find(name string) (int, bool) {
	for i, s := range b.Sounds {
		if s.Name == name {
			return i, true
		}
	}
	return 0, false
}

// Sound — байты WEM звука i.
func (b *BNK) Sound(r io.ReaderAt, i int) ([]byte, error) {
	s := b.Sounds[i]
	out := make([]byte, s.size)
	if _, err := r.ReadAt(out, s.off); err != nil && err != io.EOF {
		return nil, err
	}
	return out, nil
}
