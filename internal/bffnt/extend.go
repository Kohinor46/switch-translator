package bffnt

import (
	"fmt"
	"image"
	"math"
	"slices"
	"sort"
	"unicode"
)

// Lookalikes — кириллица, которая выглядит как латиница: такие буквы берутся из самой игры.
var Lookalikes = map[rune]rune{
	'А': 'A', 'В': 'B', 'Е': 'E', 'К': 'K', 'М': 'M', 'Н': 'H', 'О': 'O', 'Р': 'P', 'С': 'C', 'Т': 'T', 'Х': 'X',
	'Ё': 'Ë', 'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'ё': 'ë',
}

// Cyrillic — русский алфавит и типографские знаки, которые обычно нужны переводу.
var Cyrillic = []rune("АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдеёжзийклмнопрстуфхцчшщъыьэюя«»—–…№„“")

// Options — чем дополнять шрифт.
type Options struct {
	TTF   []byte // откуда рисовать буквы (TTF или OTF)
	Reuse bool   // похожие на латиницу буквы брать из игры
}

// Result — что получилось.
type Result struct {
	Data    []byte // новый шрифт; nil — дополнять было нечего
	Drawn   []rune // нарисованы из TTF
	Reused  []rune // взяты у латинских двойников
	Missing []rune // нет ни в игре, ни в TTF
	Sheets  int    // сколько листов добавлено
	Preview *image.NRGBA
}

// glyphParams — масштаб и утолщение для заглавных или строчных.
type glyphParams struct{ ppem, grow, sx float64 }

// Add дописывает в шрифт недостающие буквы. Старые листы и таблицы не меняются:
// новые буквы ложатся на новые листы, а таблицы символов дописываются в начало списка.
func (f *Font) Add(chars []rune, opt Options) (*Result, error) {
	have := f.Chars()
	res := &Result{}
	var need []rune
	seen := map[rune]bool{}
	for _, c := range chars {
		if _, ok := have[c]; ok || c <= ' ' || seen[c] {
			continue
		}
		seen[c] = true
		need = append(need, c)
	}
	if len(need) == 0 {
		return res, nil
	}
	slices.Sort(need)

	mapping := map[rune]int{} // новый код → индекс картинки
	var draw []rune
	for _, c := range need {
		if l, ok := Lookalikes[c]; ok && opt.Reuse {
			if i, ok := have[l]; ok {
				mapping[c] = i
				res.Reused = append(res.Reused, c)
				continue
			}
		}
		draw = append(draw, c)
	}

	var newSheets []*image.NRGBA
	var widths []Width
	base := f.Sheets * f.Cols * f.Rows
	if len(draw) > 0 {
		if len(opt.TTF) == 0 {
			res.Missing = append(res.Missing, draw...)
			draw = nil
		}
	}
	if len(draw) > 0 {
		t, err := loadTTF(opt.TTF)
		if err != nil {
			return nil, err
		}
		sheets := make([]*image.NRGBA, f.Sheets)
		for i := range sheets {
			sheets[i] = f.Sheet(i)
		}
		st, err := f.measureStyle(sheets, have)
		if err != nil {
			return nil, err
		}
		upper, err := t.fit('H', st.capH, st.stemU, st)
		if err != nil {
			return nil, err
		}
		lower, err := t.fit('x', st.xH, st.stemL, st)
		if err != nil {
			lower = upper
		}
		upper.sx = f.widthScale(t, have, "HNOEDU", upper, st)
		lower.sx = f.widthScale(t, have, "noaednu", lower, st)
		dl, dc := f.calibrate(t, have, upper, lower, st)

		per := f.Cols * f.Rows
		for _, c := range draw {
			if !t.has(c) {
				res.Missing = append(res.Missing, c)
				continue
			}
			p := upper
			if unicode.IsLower(c) {
				p = lower
			}
			g, err := t.renderFit(c, p, st, f.CellW)
			if err != nil {
				res.Missing = append(res.Missing, c)
				continue
			}
			k := len(widths)
			if k%per == 0 {
				newSheets = append(newSheets, image.NewNRGBA(image.Rect(0, 0, f.SheetW, f.SheetH)))
			}
			_, cell := f.cell(base + k)
			ink := g.ink()
			img := newSheets[len(newSheets)-1]
			for y := ink.Min.Y; y < ink.Max.Y; y++ {
				cy := cell.Min.Y + f.Baseline + g.y0 + y
				if cy < cell.Min.Y || cy >= cell.Max.Y {
					continue // не влезло по высоте — обрезаем
				}
				for x := ink.Min.X; x < ink.Max.X && x-ink.Min.X < f.CellW; x++ {
					a := uint8(math.Round(math.Min(255, g.alpha[y*g.w+x])))
					o := img.PixOffset(cell.Min.X+x-ink.Min.X, cy)
					img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = 255, 255, 255, a
				}
			}
			widths = append(widths, Width{
				Left:  clampI8(g.x0 + ink.Min.X + dl),
				Glyph: uint8(min(ink.Dx(), f.CellW)),
				Char:  clampU8(int(math.Round(g.advance)) + dc),
			})
			mapping[c] = base + k
			res.Drawn = append(res.Drawn, c)
		}
	}
	if len(mapping) == 0 {
		return res, nil
	}
	res.Sheets = len(newSheets)
	if len(newSheets) > 0 {
		res.Preview = newSheets[0]
	}
	if f.Sheets+len(newSheets) > 255 {
		return nil, fmt.Errorf("в шрифте не хватит места для листов")
	}
	res.Data = f.rebuild(newSheets, base, widths, mapping, have)
	return res, nil
}

// fit подбирает размер и утолщение, чтобы высота и толщина штриха буквы ref совпали с игрой.
func (t *ttf) fit(ref rune, height, stem float64, st *style) (glyphParams, error) {
	if !t.has(ref) {
		return glyphParams{}, fmt.Errorf("в TTF нет буквы %q", ref)
	}
	hEm := t.inkHeight(ref)
	const probe = 200.0
	g, err := t.render(ref, probe, 1, 0, plain)
	if err != nil || hEm == 0 {
		return glyphParams{}, fmt.Errorf("не удалось измерить %q в TTF", ref)
	}
	sEm := g.stem(hEm*probe) / probe
	// ppem·hEm + 2·grow = height,  ppem·sEm + 2·grow = stem
	p := glyphParams{ppem: height / hEm, sx: 1}
	if hEm > sEm {
		if ppem := (height - stem) / (hEm - sEm); ppem > 0 {
			p.ppem = ppem
			p.grow = (stem - ppem*sEm) / 2
		}
	}
	// утоньшать можно немного, иначе штрихи рвутся, а сильное утолщение заливает буквы внутри
	// (Ж, Ш, Ю): тогда штрих останется тоньше, чем в игре, но буквы читаются
	ring := st.ring()
	lo, hi := -p.ppem*sEm*0.3, ring+p.ppem*0.04
	if p.grow < lo || p.grow > hi {
		p.grow = math.Max(lo, math.Min(hi, p.grow))
		for range 4 { // hi зависит от размера — уточняем
			p.ppem = (height - 2*p.grow) / hEm
			p.grow = math.Max(-p.ppem*sEm*0.3, math.Min(ring+p.ppem*0.04, p.grow))
		}
	}
	return p, nil
}

// widthScale — во сколько раз расширить буквы TTF, чтобы ширина латиницы совпала с игрой.
// Без этого сильно утолщённые буквы заплывают внутри.
func (f *Font) widthScale(t *ttf, have map[rune]int, sample string, p glyphParams, st *style) float64 {
	var game, ours float64
	for _, c := range sample {
		i, ok := have[c]
		if !ok || !t.has(c) {
			continue
		}
		g, err := t.render(c, p.ppem, 1, p.grow, st)
		if err != nil {
			continue
		}
		game += float64(f.GlyphWidth(i).Glyph) - 2*p.grow
		ours += float64(g.ink().Dx()) - 2*p.grow
	}
	if game <= 0 || ours <= 0 {
		return 1
	}
	return math.Max(0.8, math.Min(1.5, game/ours))
}

// stem — толщина первого вертикального штриха на высоте 0.8 от буквы.
func (r *rendered) stem(h float64) float64 {
	y := -int(h*0.8) - r.y0
	if y < 0 || y >= r.h {
		return 0
	}
	sum, started := 0.0, false
	for x := range r.w {
		a := r.alpha[y*r.w+x] / 255
		if a > 0 {
			started = true
		} else if started {
			break
		}
		sum += a
	}
	return sum
}

// renderFit рисует букву, сжимая по ширине, если она не влезает в клетку.
func (t *ttf) renderFit(c rune, p glyphParams, st *style, cellW int) (*rendered, error) {
	sx := p.sx
	for range 4 {
		g, err := t.render(c, p.ppem, sx, p.grow, st)
		if err != nil {
			return nil, err
		}
		if w := g.ink().Dx(); w > cellW {
			sx *= float64(cellW) / float64(w) * 0.97
			continue
		}
		return g, nil
	}
	return t.render(c, p.ppem, sx, p.grow, st)
}

// calibrate сравнивает отступы латинских букв игры и тех же букв из TTF: на столько же сдвигаем новые.
func (f *Font) calibrate(t *ttf, have map[rune]int, upper, lower glyphParams, st *style) (dl, dc int) {
	var sl, sc, n float64
	for _, c := range "HNOEnoaedu" {
		i, ok := have[c]
		if !ok || !t.has(c) {
			continue
		}
		p := upper
		if unicode.IsLower(c) {
			p = lower
		}
		g, err := t.render(c, p.ppem, p.sx, p.grow, st)
		if err != nil {
			continue
		}
		w := f.GlyphWidth(i)
		sl += float64(int(w.Left) - (g.x0 + g.ink().Min.X))
		sc += float64(int(w.Char)) - math.Round(g.advance)
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return int(math.Round(sl / n)), int(math.Round(sc / n))
}

func clampI8(v int) int8  { return int8(max(-128, min(127, v))) }
func clampU8(v int) uint8 { return uint8(max(0, min(255, v))) }

// rebuild собирает файл: TGLP с новыми листами, новый CWDH после последнего, новые CMAP — в начало списка.
func (f *Font) rebuild(sheets []*image.NRGBA, base int, widths []Width, mapping map[rune]int, have map[rune]int) []byte {
	b := f.raw
	// новый TGLP
	tex := f.tex.raw
	maxW := f.MaxCharW
	if len(sheets) > 0 {
		tex = f.tex.withLayers(sheets)
		for _, w := range widths {
			maxW = max(maxW, int(w.Char))
		}
	}
	count := f.Sheets + len(sheets)
	sheetSize := divUp(len(tex), count)
	tglp := append([]byte(nil), b[f.tglp:f.sheetData]...)
	tglp = append(tglp, tex...)
	tglp = append(tglp, make([]byte, sheetSize*count-len(tex))...)
	tglp = pad4(tglp)
	le.PutUint32(tglp[4:], uint32(len(tglp)))
	tglp[10] = byte(count)
	tglp[11] = byte(min(255, maxW))
	le.PutUint32(tglp[12:], uint32(sheetSize))

	// новые таблицы
	var cwdh []byte
	if len(widths) > 0 {
		cwdh = []byte("CWDH\x00\x00\x00\x00")
		cwdh = le.AppendUint16(cwdh, uint16(base))
		cwdh = le.AppendUint16(cwdh, uint16(base+len(widths)-1))
		cwdh = le.AppendUint32(cwdh, 0)
		for _, w := range widths {
			cwdh = append(cwdh, byte(w.Left), w.Glyph, w.Char)
		}
		cwdh = pad4(cwdh)
		le.PutUint32(cwdh[4:], uint32(len(cwdh)))
	}
	cmaps := newCmaps(mapping, have)

	// раскладка: старые блоки по порядку, новые — после последнего блока своего вида
	lastCWDH, lastCMAP := -1, -1
	for i, bl := range f.blocks {
		switch bl.magic {
		case "CWDH":
			lastCWDH = i
		case "CMAP":
			lastCMAP = i
		}
	}
	out := append([]byte(nil), b[:f.hdr]...)
	newOff := make([]int, len(f.blocks))
	cwdhAt, cmapAt := 0, []int{}
	blocks := len(f.blocks)
	for i, bl := range f.blocks {
		newOff[i] = len(out)
		if bl.off == f.tglp {
			out = append(out, tglp...)
		} else {
			out = append(out, b[bl.off:bl.off+bl.size]...)
		}
		if i == lastCWDH && cwdh != nil {
			cwdhAt = len(out)
			out = append(out, cwdh...)
			blocks++
		}
		if i == lastCMAP {
			for _, m := range cmaps {
				cmapAt = append(cmapAt, len(out))
				out = append(out, m...)
				blocks++
			}
		}
	}
	remap := func(p int) int {
		if p == 0 {
			return 0
		}
		for i, bl := range f.blocks {
			if bl.off <= p && p < bl.off+bl.size {
				return newOff[i] + p - bl.off
			}
		}
		return p
	}
	put := func(at, v int) { le.PutUint32(out[at:], uint32(v)) }

	// FINF
	fi := remap(f.finf)
	put(fi+0x14, remap(int(le.Uint32(b[f.finf+0x14:]))))
	put(fi+0x18, remap(int(le.Uint32(b[f.finf+0x18:]))))
	oldHead := remap(int(le.Uint32(b[f.finf+0x1C:])))
	if len(cmapAt) > 0 {
		put(fi+0x1C, cmapAt[0]+8)
	} else {
		put(fi+0x1C, oldHead)
	}
	// TGLP: начало листов
	put(remap(f.tglp)+0x1C, remap(f.sheetData))
	// цепочки CWDH и CMAP
	for _, w := range f.widths {
		nx := int(le.Uint32(b[w.data-4:]))
		at := remap(w.data - 4)
		if nx == 0 && cwdh != nil {
			put(at, cwdhAt+8)
		} else {
			put(at, remap(nx))
		}
	}
	for _, m := range f.maps {
		put(remap(m.data-4), remap(int(le.Uint32(b[m.data-4:]))))
	}
	for i, at := range cmapAt {
		next := oldHead
		if i+1 < len(cmapAt) {
			next = cmapAt[i+1] + 8
		}
		put(at+20, next)
	}
	put(0x0C, len(out))
	le.PutUint16(out[0x10:], uint16(blocks))
	return out
}

// newCmaps — таблицы «код → индекс» (поиск по списку). Диапазон таблицы не должен
// захватывать старые буквы: консоль смотрит только первую таблицу, в чей диапазон попал код.
func newCmaps(mapping map[rune]int, have map[rune]int) [][]byte {
	codes := make([]rune, 0, len(mapping))
	for c := range mapping {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	old := make([]rune, 0, len(have))
	for c := range have {
		old = append(old, c)
	}
	slices.Sort(old)
	between := func(a, b rune) bool { // есть ли старая буква строго между a и b
		i := sort.Search(len(old), func(i int) bool { return old[i] > a })
		return i < len(old) && old[i] < b
	}
	var groups [][]rune
	for _, c := range codes {
		if n := len(groups); n > 0 && !between(groups[n-1][len(groups[n-1])-1], c) {
			groups[n-1] = append(groups[n-1], c)
			continue
		}
		groups = append(groups, []rune{c})
	}
	var out [][]byte
	for _, g := range groups {
		m := []byte("CMAP\x00\x00\x00\x00")
		m = le.AppendUint32(m, uint32(g[0]))
		m = le.AppendUint32(m, uint32(g[len(g)-1]))
		m = le.AppendUint16(m, 2)
		m = le.AppendUint16(m, 0)
		m = le.AppendUint32(m, 0) // следующая — проставится при сборке
		m = le.AppendUint16(m, uint16(len(g)))
		m = le.AppendUint16(m, 0)
		for _, c := range g {
			m = le.AppendUint32(m, uint32(c))
			m = le.AppendUint16(m, uint16(mapping[c]))
			m = le.AppendUint16(m, 0)
		}
		m = pad4(m)
		le.PutUint32(m[4:], uint32(len(m)))
		out = append(out, m)
	}
	return out
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
