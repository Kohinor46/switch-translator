// Package bffnt читает шрифты Nintendo для Switch (BFFNT 4.x) и дописывает в них новые буквы.
package bffnt

import (
	_ "embed"
	"fmt"
	"image"
	"sort"
)

// Width — метрики буквы: отступ от пера до картинки, ширина картинки и шаг пера.
type Width struct {
	Left  int8
	Glyph uint8
	Char  uint8
}

type block struct {
	magic     string
	off, size int
}

type cmapRange struct {
	begin, end rune
	method     uint16
	data       int // начало данных после заголовка
}

type cwdhRange struct {
	start, end int
	data       int
}

// Font — разобранный шрифт.
type Font struct {
	raw    []byte
	hdr    int
	blocks []block

	finf, tglp int // начала блоков
	// FINF
	Height, Width, Ascent int
	LineFeed              int
	Default               Width
	// TGLP
	CellW, CellH int
	Sheets       int
	MaxCharW     int
	Baseline     int
	Cols, Rows   int
	SheetW       int
	SheetH       int
	sheetData    int
	sheetSize    int
	tex          *bntx

	maps   []cmapRange
	widths []cwdhRange
}

// Parse разбирает BFFNT версии 4 (Switch).
func Parse(b []byte) (*Font, error) {
	if len(b) < 0x14 || string(b[:4]) != "FFNT" {
		return nil, fmt.Errorf("это не BFFNT")
	}
	if b[4] != 0xFF || b[5] != 0xFE {
		return nil, fmt.Errorf("шрифт не для Switch (порядок байт big-endian)")
	}
	if v := le.Uint32(b[8:]); v < 0x04000000 {
		return nil, fmt.Errorf("шрифт версии %X — не для Switch", v)
	}
	f := &Font{raw: b, hdr: int(le.Uint16(b[6:]))}
	for o := f.hdr; o+8 <= len(b); {
		size := int(le.Uint32(b[o+4:]))
		if size < 8 || o+size > len(b) {
			return nil, fmt.Errorf("испорченный блок %q", b[o:o+4])
		}
		f.blocks = append(f.blocks, block{string(b[o : o+4]), o, size})
		switch string(b[o : o+4]) {
		case "FINF":
			f.finf = o
		case "TGLP":
			f.tglp = o
		}
		o += size
	}
	if f.finf == 0 || f.tglp == 0 || f.finf+0x20 > len(b) || f.tglp+0x20 > len(b) {
		return nil, fmt.Errorf("в шрифте нет FINF или TGLP")
	}
	fi := b[f.finf:]
	f.Height, f.Width, f.Ascent = int(fi[9]), int(fi[10]), int(fi[11])
	f.LineFeed = int(le.Uint16(fi[12:]))
	f.Default = Width{int8(fi[16]), fi[17], fi[18]}
	tg := b[f.tglp:]
	f.CellW, f.CellH, f.Sheets, f.MaxCharW = int(tg[8]), int(tg[9]), int(tg[10]), int(tg[11])
	f.sheetSize = int(le.Uint32(tg[12:]))
	f.Baseline = int(le.Uint16(tg[16:]))
	f.Cols, f.Rows = int(le.Uint16(tg[20:])), int(le.Uint16(tg[22:]))
	f.SheetW, f.SheetH = int(le.Uint16(tg[24:])), int(le.Uint16(tg[26:]))
	f.sheetData = int(le.Uint32(tg[28:]))
	end := f.tglp + int(le.Uint32(tg[4:]))
	if f.sheetData >= end || f.Sheets < 1 || f.Cols < 1 || f.Rows < 1 {
		return nil, fmt.Errorf("испорченный TGLP")
	}
	var err error
	if f.tex, err = parseBNTX(b[f.sheetData:end]); err != nil {
		return nil, err
	}
	if f.tex.layers != f.Sheets || f.tex.w != f.SheetW || f.tex.h != f.SheetH {
		return nil, fmt.Errorf("листы шрифта не совпадают с TGLP")
	}

	for p, guard := int(le.Uint32(fi[0x1C:])), 0; p != 0 && guard < 1000; guard++ {
		if p+16 > len(b) {
			return nil, fmt.Errorf("испорченный CMAP")
		}
		f.maps = append(f.maps, cmapRange{rune(le.Uint32(b[p:])), rune(le.Uint32(b[p+4:])), le.Uint16(b[p+8:]), p + 16})
		p = int(le.Uint32(b[p+12:]))
	}
	for p, guard := int(le.Uint32(fi[0x18:])), 0; p != 0 && guard < 1000; guard++ {
		if p+8 > len(b) {
			return nil, fmt.Errorf("испорченный CWDH")
		}
		f.widths = append(f.widths, cwdhRange{int(le.Uint16(b[p:])), int(le.Uint16(b[p+2:])), p + 8})
		p = int(le.Uint32(b[p+4:]))
	}
	return f, nil
}

// lookupIn — индекс буквы в одной таблице CMAP, -1 — нет.
func (f *Font) lookupIn(m cmapRange, c rune) int {
	b := f.raw
	switch m.method {
	case 0:
		return int(le.Uint16(b[m.data:])) + int(c-m.begin)
	case 1:
		i := m.data + int(c-m.begin)*2
		if i+2 > len(b) {
			return -1
		}
		if v := le.Uint16(b[i:]); v != 0xFFFF {
			return int(v)
		}
	case 2:
		n := int(le.Uint16(b[m.data:]))
		i := sort.Search(n, func(i int) bool { return rune(le.Uint32(b[m.data+4+i*8:])) >= c })
		if i < n && rune(le.Uint32(b[m.data+4+i*8:])) == c {
			return int(le.Uint16(b[m.data+4+i*8+4:]))
		}
	}
	return -1
}

// Index — как консоль ищет букву: первая таблица, в чей диапазон попал код, и только она.
func (f *Font) Index(c rune) int {
	for _, m := range f.maps {
		if m.begin <= c && c <= m.end {
			return f.lookupIn(m, c)
		}
	}
	return -1
}

// Chars — все буквы, которые консоль найдёт в шрифте.
func (f *Font) Chars() map[rune]int {
	out := map[rune]int{}
	add := func(c rune, i int) {
		if _, ok := out[c]; !ok && i >= 0 && f.Index(c) == i {
			out[c] = i
		}
	}
	b := f.raw
	for _, m := range f.maps {
		switch m.method {
		case 0, 1:
			for c := m.begin; c <= m.end && c-m.begin < 0x10000; c++ {
				add(c, f.lookupIn(m, c))
			}
		case 2:
			n := int(le.Uint16(b[m.data:]))
			for i := range n {
				e := m.data + 4 + i*8
				add(rune(le.Uint32(b[e:])), int(le.Uint16(b[e+4:])))
			}
		}
	}
	return out
}

// GlyphWidth — метрики буквы по индексу.
func (f *Font) GlyphWidth(i int) Width {
	for _, w := range f.widths {
		if w.start <= i && i <= w.end {
			e := w.data + (i-w.start)*3
			return Width{int8(f.raw[e]), f.raw[e+1], f.raw[e+2]}
		}
	}
	return f.Default
}

// glyphCount — первый свободный индекс после всех нарисованных букв.
func (f *Font) glyphCount() int {
	n := 0
	for _, w := range f.widths {
		n = max(n, w.end+1)
	}
	return n
}

// cell — где на листе лежит клетка буквы: между клетками и по краю — по пикселю.
func (f *Font) cell(i int) (sheet int, r image.Rectangle) {
	per := f.Cols * f.Rows
	sheet, i = i/per, i%per
	x := (i%f.Cols)*(f.CellW+1) + 1
	y := (i/f.Cols)*(f.CellH+1) + 1
	return sheet, image.Rect(x, y, x+f.CellW, y+f.CellH)
}

// Sheet — лист шрифта картинкой (белые буквы на прозрачном).
func (f *Font) Sheet(i int) *image.NRGBA { return f.tex.Layer(i) }

// Glyph — картинка буквы размером в клетку; базовая линия на высоте Baseline от верха.
func (f *Font) Glyph(sheets []*image.NRGBA, i int) *image.NRGBA {
	s, r := f.cell(i)
	if s >= len(sheets) {
		return nil
	}
	return sheets[s].SubImage(r).(*image.NRGBA)
}

// Format — формат листов, для сообщений.
func (f *Font) Format() string { return f.tex.pf.name }

// Render набирает строку этим шрифтом — для предпросмотра. Неизвестные буквы пропускаются.
func (f *Font) Render(text string) *image.NRGBA {
	sheets := make([]*image.NRGBA, f.Sheets)
	for i := range sheets {
		sheets[i] = f.Sheet(i)
	}
	pen, w := 0, 0
	type placed struct {
		g *image.NRGBA
		x int
	}
	var list []placed
	for _, c := range text {
		i := f.Index(c)
		if i < 0 {
			pen += int(f.Default.Char)
			continue
		}
		gw := f.GlyphWidth(i)
		if g := f.Glyph(sheets, i); g != nil {
			list = append(list, placed{g, pen + int(gw.Left)})
			w = max(w, pen+int(gw.Left)+f.CellW)
		}
		pen += int(gw.Char)
	}
	img := image.NewNRGBA(image.Rect(0, 0, max(w, pen, 1), f.CellH))
	for _, p := range list {
		r := p.g.Rect
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				c := p.g.NRGBAAt(x, y)
				dx, dy := p.x+x-r.Min.X, y-r.Min.Y
				if c.A > img.NRGBAAt(dx, dy).A {
					img.SetNRGBA(dx, dy, c)
				}
			}
		}
	}
	return img
}

//go:embed fonts/NotoSans-Bold.ttf
var defaultTTF []byte

// DefaultTTF — встроенный шрифт для новых букв (Noto Sans Bold, лицензия OFL — fonts/OFL.txt).
func DefaultTTF() []byte { return defaultTTF }
