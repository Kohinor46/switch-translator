package bffnt

import (
	"fmt"
	"image"
	"image/color"
)

// Листы шрифта на Switch — текстуры в «блочно-линейной» раскладке Tegra X1 (swizzle),
// чаще всего в сжатии BC4 (один канал — прозрачность).

// pixelFormat — как хранятся пиксели: размер блока и байты на блок.
type pixelFormat struct {
	name   string
	block  int // сторона блока в пикселях (1 — без сжатия)
	bytes  int // байт на блок
	decode func(src []byte, img *image.NRGBA, x, y int)
	encode func(img *image.NRGBA, x, y int, dst []byte)
}

// Формат BNTX: старший байт — вид, младший — тип (1 = UNORM, 6 = SRGB).
var formats = map[uint16]pixelFormat{
	0x02: {"R8", 1, 1, decodeR8, encodeR8},
	0x09: {"R8G8", 1, 2, decodeRG8, encodeRG8},
	0x0B: {"R8G8B8A8", 1, 4, decodeRGBA8, encodeRGBA8},
	0x1A: {"BC1", 4, 8, decodeBC1, encodeBC1},
	0x1C: {"BC3", 4, 16, decodeBC3, encodeBC3},
	0x1D: {"BC4", 4, 8, decodeBC4, encodeBC4},
}

func formatOf(f uint32) (pixelFormat, error) {
	pf, ok := formats[uint16(f>>8)]
	if !ok {
		return pixelFormat{}, fmt.Errorf("формат листа шрифта %04X не поддерживается", f)
	}
	return pf, nil
}

func divUp(n, d int) int   { return (n + d - 1) / d }
func roundUp(n, d int) int { return divUp(n, d) * d }

// layerSize — сколько байт занимает один лист в блочно-линейной раскладке.
func layerSize(w, h int, pf pixelFormat, blockHeight int) int {
	bw, bh := divUp(w, pf.block), divUp(h, pf.block)
	return roundUp(bw*pf.bytes, 64) * roundUp(bh, 8*blockHeight)
}

// blockAddr — адрес блока (x, y) в раскладке Tegra X1: GOB 64×8 байт, столбики по blockHeight GOB.
func blockAddr(x, y, widthBlocks, bpp, blockHeight int) int {
	gobsWide := divUp(widthBlocks*bpp, 64)
	addr := (y/(8*blockHeight))*512*blockHeight*gobsWide +
		(x*bpp/64)*512*blockHeight +
		(y%(8*blockHeight)/8)*512
	x *= bpp
	return addr + (x%64)/32*256 + (y%8)/2*64 + (x%32)/16*32 + y%2*16 + x%16
}

// decodeLayer превращает лист в картинку.
func decodeLayer(data []byte, w, h int, pf pixelFormat, blockHeight int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bw, bh := divUp(w, pf.block), divUp(h, pf.block)
	for y := range bh {
		for x := range bw {
			a := blockAddr(x, y, bw, pf.bytes, blockHeight)
			if a+pf.bytes > len(data) {
				continue
			}
			pf.decode(data[a:a+pf.bytes], img, x*pf.block, y*pf.block)
		}
	}
	return img
}

// encodeLayer — обратно: картинка в лист той же раскладки.
func encodeLayer(img *image.NRGBA, pf pixelFormat, blockHeight int) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := make([]byte, layerSize(w, h, pf, blockHeight))
	bw, bh := divUp(w, pf.block), divUp(h, pf.block)
	for y := range bh {
		for x := range bw {
			a := blockAddr(x, y, bw, pf.bytes, blockHeight)
			pf.encode(img, x*pf.block, y*pf.block, out[a:a+pf.bytes])
		}
	}
	return out
}

// blockHeightFor — высота столбика в GOB, которую выбирает инструментарий Nintendo для текстуры.
func blockHeightFor(h int, pf pixelFormat) int {
	rows := divUp(divUp(h, pf.block), 8)
	bh := 1
	for bh < rows && bh < 16 {
		bh <<= 1
	}
	return bh
}

// Шрифт в листе — белый с прозрачностью; одноканальные форматы хранят только прозрачность.

func decodeR8(s []byte, img *image.NRGBA, x, y int) {
	img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, s[0]})
}

func encodeR8(img *image.NRGBA, x, y int, d []byte) { d[0] = alphaAt(img, x, y) }

func decodeRG8(s []byte, img *image.NRGBA, x, y int) {
	img.SetNRGBA(x, y, color.NRGBA{s[0], s[0], s[0], s[1]})
}

func encodeRG8(img *image.NRGBA, x, y int, d []byte) {
	c := img.NRGBAAt(x, y)
	d[0], d[1] = c.R, c.A
}

func decodeRGBA8(s []byte, img *image.NRGBA, x, y int) {
	img.SetNRGBA(x, y, color.NRGBA{s[0], s[1], s[2], s[3]})
}

func encodeRGBA8(img *image.NRGBA, x, y int, d []byte) {
	c := img.NRGBAAt(x, y)
	d[0], d[1], d[2], d[3] = c.R, c.G, c.B, c.A
}

func alphaAt(img *image.NRGBA, x, y int) uint8 {
	if !(image.Point{x, y}.In(img.Rect)) {
		return 0
	}
	return img.Pix[img.PixOffset(x, y)+3]
}

// BC4: два опорных значения и 16 трёхбитных индексов.
func bc4Palette(r0, r1 uint8) [8]uint8 {
	p := [8]uint8{r0, r1}
	a, b := int(r0), int(r1)
	if r0 > r1 {
		for i := 1; i <= 6; i++ {
			p[i+1] = uint8(((7-i)*a + i*b + 3) / 7)
		}
	} else {
		for i := 1; i <= 4; i++ {
			p[i+1] = uint8(((5-i)*a + i*b + 2) / 5)
		}
		p[6], p[7] = 0, 255
	}
	return p
}

func decodeBC4Values(s []byte) [16]uint8 {
	p := bc4Palette(s[0], s[1])
	var bits uint64
	for i := 7; i >= 2; i-- {
		bits = bits<<8 | uint64(s[i])
	}
	var v [16]uint8
	for i := range 16 {
		v[i] = p[bits>>(3*i)&7]
	}
	return v
}

func decodeBC4(s []byte, img *image.NRGBA, x, y int) {
	v := decodeBC4Values(s)
	for i := range 16 {
		img.SetNRGBA(x+i%4, y+i/4, color.NRGBA{255, 255, 255, v[i]})
	}
}

func encodeBC4(img *image.NRGBA, x, y int, d []byte) {
	var v [16]uint8
	for i := range 16 {
		v[i] = alphaAt(img, x+i%4, y+i/4)
	}
	encodeBC4Values(v, d)
}

// encodeBC4Values подбирает лучший из двух режимов: 8 оттенков между краями
// или 6 оттенков плюс точные 0 и 255 (удобно для краёв букв).
func encodeBC4Values(v [16]uint8, d []byte) {
	lo, hi := uint8(255), uint8(0)
	ilo, ihi := uint8(255), uint8(0) // без 0 и 255
	for _, c := range v {
		lo, hi = min(lo, c), max(hi, c)
		if c != 0 && c != 255 {
			ilo, ihi = min(ilo, c), max(ihi, c)
		}
	}
	if lo == hi {
		d[0], d[1] = hi, lo
		clear(d[2:8])
		return
	}
	best := make([]byte, 8)
	bestErr := -1
	try := func(r0, r1 uint8) {
		p := bc4Palette(r0, r1)
		var bits uint64
		e := 0
		for i, c := range v {
			bi, be := 0, 1<<30
			for j, pc := range p {
				if dd := int(c) - int(pc); dd*dd < be {
					bi, be = j, dd*dd
				}
			}
			e += be
			bits |= uint64(bi) << (3 * i)
		}
		if bestErr < 0 || e < bestErr {
			bestErr = e
			best[0], best[1] = r0, r1
			for i := range 6 {
				best[2+i] = byte(bits >> (8 * i))
			}
		}
	}
	try(hi, lo)
	if ilo <= ihi {
		try(ilo, ihi) // r0 <= r1 — режим с 0 и 255
	} else {
		try(lo, lo) // только 0 и 255
	}
	copy(d, best)
}

// BC1/BC3 в шрифтах встречаются редко; цвет пишем белым, прозрачность — как в BC4 (BC3) или по порогу (BC1).
func rgb565(c uint16) (uint8, uint8, uint8) {
	r, g, b := c>>11&31, c>>5&63, c&31
	return uint8(r<<3 | r>>2), uint8(g<<2 | g>>4), uint8(b<<3 | b>>2)
}

func bc1Colors(s []byte) ([4][4]uint8, bool) {
	c0, c1 := uint16(s[0])|uint16(s[1])<<8, uint16(s[2])|uint16(s[3])<<8
	var p [4][4]uint8
	p[0][0], p[0][1], p[0][2] = rgb565(c0)
	p[1][0], p[1][1], p[1][2] = rgb565(c1)
	p[0][3], p[1][3] = 255, 255
	opaque := c0 > c1
	for k := range 3 {
		a, b := int(p[0][k]), int(p[1][k])
		if opaque {
			p[2][k] = uint8((2*a + b) / 3)
			p[3][k] = uint8((a + 2*b) / 3)
		} else {
			p[2][k] = uint8((a + b) / 2)
		}
	}
	p[2][3] = 255
	if opaque {
		p[3][3] = 255
	}
	return p, opaque
}

func decodeBC1(s []byte, img *image.NRGBA, x, y int) {
	p, _ := bc1Colors(s)
	for i := range 16 {
		c := p[s[4+i/4]>>(2*(i%4))&3]
		img.SetNRGBA(x+i%4, y+i/4, color.NRGBA{c[0], c[1], c[2], c[3]})
	}
}

func encodeBC1(img *image.NRGBA, x, y int, d []byte) {
	// белый (0xFFFF) и прозрачный: c0 <= c1 включает режим с прозрачным индексом 3
	d[0], d[1], d[2], d[3] = 0xFF, 0xFF, 0xFF, 0xFF
	for row := range 4 {
		var b byte
		for col := range 4 {
			idx := byte(0)
			if alphaAt(img, x+col, y+row) < 128 {
				idx = 3
			}
			b |= idx << (2 * col)
		}
		d[4+row] = b
	}
}

func decodeBC3(s []byte, img *image.NRGBA, x, y int) {
	a := decodeBC4Values(s[:8])
	decodeBC1(s[8:], img, x, y)
	for i := range 16 {
		o := img.PixOffset(x+i%4, y+i/4)
		if o >= 0 && o+3 < len(img.Pix) && (image.Point{x + i%4, y + i/4}.In(img.Rect)) {
			img.Pix[o+3] = a[i]
		}
	}
}

func encodeBC3(img *image.NRGBA, x, y int, d []byte) {
	encodeBC4(img, x, y, d[:8])
	d[8], d[9], d[10], d[11] = 0xFF, 0xFF, 0xFF, 0xFF
	clear(d[12:16]) // все пиксели — цвет 0 (белый)
}
