package bffnt

import (
	"encoding/binary"
	"fmt"
	"image"
)

// bntx — контейнер текстур Switch внутри TGLP: одна текстура-массив, лист шрифта — слой массива.
type bntx struct {
	raw         []byte
	brti        int // начало описания текстуры
	dataStart   int // начало пикселей
	reloc       int // таблица перемещений _RLT (после пикселей)
	pf          pixelFormat
	format      uint32
	w, h        int
	layers      int
	blockHeight int
	layer       int // байт на слой
}

var le = binary.LittleEndian

func parseBNTX(b []byte) (*bntx, error) {
	if len(b) < 0x50 || string(b[:4]) != "BNTX" || string(b[0x20:0x24]) != "NX  " {
		return nil, fmt.Errorf("листы шрифта не в BNTX")
	}
	if b[0xC] != 0xFF || b[0xD] != 0xFE {
		return nil, fmt.Errorf("BNTX с обратным порядком байт не поддерживается")
	}
	if n := le.Uint32(b[0x24:]); n != 1 {
		return nil, fmt.Errorf("в BNTX шрифта %d текстур вместо одной", n)
	}
	t := &bntx{raw: b, reloc: int(le.Uint32(b[0x18:]))}
	infos := int(le.Uint64(b[0x28:]))
	if infos+8 > len(b) {
		return nil, fmt.Errorf("BNTX обрезан")
	}
	t.brti = int(le.Uint64(b[infos:]))
	r := t.brti
	if r+0x90 > len(b) || string(b[r:r+4]) != "BRTI" {
		return nil, fmt.Errorf("в BNTX нет описания текстуры")
	}
	if mips := le.Uint16(b[r+0x16:]); mips != 1 {
		return nil, fmt.Errorf("у листов шрифта %d уровней детализации", mips)
	}
	if tile := le.Uint16(b[r+0x12:]); tile != 0 {
		return nil, fmt.Errorf("листы шрифта в линейной раскладке не поддерживаются")
	}
	t.format = le.Uint32(b[r+0x1C:])
	var err error
	if t.pf, err = formatOf(t.format); err != nil {
		return nil, err
	}
	t.w, t.h = int(le.Uint32(b[r+0x24:])), int(le.Uint32(b[r+0x28:]))
	t.layers = int(le.Uint32(b[r+0x30:]))
	t.blockHeight = 1 << le.Uint32(b[r+0x34:])
	t.layer = layerSize(t.w, t.h, t.pf, t.blockHeight)
	ptrs := int(le.Uint64(b[r+0x70:]))
	if ptrs+8 > len(b) {
		return nil, fmt.Errorf("BNTX обрезан")
	}
	t.dataStart = int(le.Uint64(b[ptrs:]))
	size := int(le.Uint32(b[r+0x50:]))
	if size != t.layers*t.layer || t.dataStart+size > t.reloc || t.reloc+0x10 > len(b) || string(b[t.reloc:t.reloc+4]) != "_RLT" {
		return nil, fmt.Errorf("неожиданное устройство BNTX (%d×%d, %d листов, %d байт)", t.w, t.h, t.layers, size)
	}
	return t, nil
}

// Layer — лист шрифта картинкой. Листы хранятся вверх ногами (начало координат текстуры — снизу).
func (t *bntx) Layer(i int) *image.NRGBA {
	o := t.dataStart + i*t.layer
	return flip(decodeLayer(t.raw[o:o+t.layer], t.w, t.h, t.pf, t.blockHeight))
}

// flip переворачивает картинку по вертикали (на месте) и возвращает её же.
func flip(img *image.NRGBA) *image.NRGBA {
	h, row := img.Rect.Dy(), img.Stride
	tmp := make([]byte, row)
	for y := range h / 2 {
		a, b := img.Pix[y*row:(y+1)*row], img.Pix[(h-1-y)*row:(h-y)*row]
		copy(tmp, a)
		copy(a, b)
		copy(b, tmp)
	}
	return img
}

// withLayers добавляет листы в конец массива: старые байты не трогаются, таблица перемещений сдвигается.
func (t *bntx) withLayers(add []*image.NRGBA) []byte {
	oldEnd := t.dataStart + t.layers*t.layer
	var extra []byte
	for _, img := range add {
		up := image.NewNRGBA(img.Rect)
		copy(up.Pix, img.Pix)
		extra = append(extra, encodeLayer(flip(up), t.pf, t.blockHeight)...)
	}
	delta := len(extra)
	out := make([]byte, 0, len(t.raw)+delta)
	out = append(out, t.raw[:oldEnd]...)
	out = append(out, extra...)
	out = append(out, t.raw[oldEnd:]...)

	r := t.brti
	le.PutUint32(out[r+0x30:], uint32(t.layers+len(add)))
	le.PutUint32(out[r+0x50:], uint32((t.layers+len(add))*t.layer))
	// блок BRTD — заголовок 0x10 прямо перед пикселями
	if brtd := t.dataStart - 0x10; brtd >= 0 && string(out[brtd:brtd+4]) == "BRTD" {
		le.PutUint64(out[brtd+8:], le.Uint64(out[brtd+8:])+uint64(delta))
	}
	rlt := t.reloc + delta
	le.PutUint32(out[0x18:], uint32(rlt))
	le.PutUint32(out[0x1C:], le.Uint32(out[0x1C:])+uint32(delta))
	le.PutUint32(out[rlt+4:], uint32(rlt))
	// разделы перемещений: тот, что покрывает пиксели, растёт; лежащие после — сдвигаются
	sections := int(le.Uint32(out[rlt+8:]))
	for i := range sections {
		s := rlt + 0x10 + i*0x18
		off, size := int(le.Uint32(out[s+8:])), int(le.Uint32(out[s+12:]))
		switch {
		case off >= oldEnd:
			le.PutUint32(out[s+8:], uint32(off+delta))
		case off+size >= oldEnd:
			le.PutUint32(out[s+12:], uint32(size+delta))
		}
	}
	return out
}
