package bffnt

import (
	"fmt"
	"image"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Новые буквы рисуются из TTF/OTF так, чтобы походить на буквы игры: та же высота заглавных
// и строчных, та же толщина штриха, та же обводка и такие же отступы между буквами.

const ss = 4 // сглаживание: каждый пиксель считается по 4×4 точкам

// style — как выглядят буквы игры. Обводка, двойная обводка и простая заливка описываются
// одинаково: яркостью штриха в зависимости от глубины — сколько пикселей от внешнего края.
type style struct {
	profile      []float64 // яркость на глубине k..k+1 пикселей от края; дальше — последнее значение
	capH, xH     float64   // высота заглавных и строчных вместе с обводкой
	stemU, stemL float64   // ширина вертикального штриха вместе с обводкой
}

var plain = &style{profile: []float64{255}}

func (s *style) at(depth float64) float64 {
	p := s.profile
	x := depth - 0.5
	if x <= 0 {
		return p[0]
	}
	i := int(x)
	if i+1 >= len(p) {
		return p[len(p)-1]
	}
	t := x - float64(i)
	return p[i]*(1-t) + p[i+1]*t
}

// ring — толщина внешней обводки: глубина, на которой штрих впервые становится самым ярким.
func (s *style) ring() float64 {
	top := 0.0
	for _, v := range s.profile {
		top = math.Max(top, v)
	}
	for k, v := range s.profile {
		if v >= top*0.9 {
			return float64(k)
		}
	}
	return 0
}

// measureStyle смотрит на латинские буквы шрифта: H, x и одиночный штрих l/I.
func (f *Font) measureStyle(sheets []*image.NRGBA, chars map[rune]int) (*style, error) {
	g := func(r rune) *image.NRGBA {
		if i, ok := chars[r]; ok {
			return f.Glyph(sheets, i)
		}
		return nil
	}
	H := g('H')
	if H == nil {
		return nil, fmt.Errorf("в шрифте нет латинской H — не с чем сравнить")
	}
	// высота по строкам, где есть что-то заметное
	height := func(img *image.NRGBA, thr uint8) float64 {
		top, bot := -1, -1
		for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
			for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
				if img.NRGBAAt(x, y).A >= thr {
					if top < 0 {
						top = y
					}
					bot = y
					break
				}
			}
		}
		if top < 0 {
			return 0
		}
		return float64(bot - top + 1)
	}
	// первый непрерывный кусок строки на доле frac от верха буквы
	run := func(img *image.NRGBA, frac float64) []float64 {
		if img == nil {
			return nil
		}
		top, bot := -1, -1
		for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
			for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
				if img.NRGBAAt(x, y).A >= 32 {
					if top < 0 {
						top = y
					}
					bot = y
					break
				}
			}
		}
		if top < 0 {
			return nil
		}
		y := top + int(float64(bot-top)*frac)
		var v []float64
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			a := float64(img.NRGBAAt(x, y).A)
			if a > 0 {
				v = append(v, a)
			} else if len(v) > 0 {
				break
			}
		}
		return v
	}
	// одиночный штрих — посередине l или I; у H — на четверти высоты, выше перекладины
	stemOf := func(c rune) []float64 {
		if c == 'H' {
			return run(H, 0.25)
		}
		return run(g(c), 0.5)
	}
	var stem []float64
	for _, c := range "lIH" {
		if stem = stemOf(c); len(stem) >= 3 {
			break
		}
	}
	if len(stem) < 3 {
		return nil, fmt.Errorf("не удалось измерить штрих букв шрифта")
	}
	// профиль: левая половина штриха, от края к середине
	prof := append([]float64(nil), stem[:(len(stem)+1)/2]...)
	// крайний пиксель — сглаживание, а не часть рисунка
	if len(prof) > 1 && prof[0] < prof[1]*0.6 {
		prof = prof[1:]
	}
	st := &style{profile: prof}
	thr := uint8(prof[0] / 2)
	width := func(v []float64) float64 {
		w := 0.0
		for _, a := range v {
			w += math.Min(1, a/prof[0])
		}
		return w
	}
	st.capH = height(H, thr)
	if x := g('x'); x != nil {
		st.xH = height(x, thr)
	}
	if st.xH == 0 {
		st.xH = st.capH * 0.72
	}
	st.stemL = width(stemOf('l'))
	st.stemU = width(stemOf('I'))
	if st.stemU == 0 {
		st.stemU = st.stemL
	}
	if st.stemL == 0 {
		st.stemL = st.stemU
	}
	if st.stemU == 0 {
		st.stemU = width(stem)
		st.stemL = st.stemU
	}
	if st.capH < 4 {
		return nil, fmt.Errorf("не удалось измерить буквы шрифта")
	}
	return st, nil
}

// ttf — шрифт, из которого берутся новые буквы.
type ttf struct {
	f   *sfnt.Font
	buf sfnt.Buffer
}

func loadTTF(b []byte) (*ttf, error) {
	f, err := sfnt.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("шрифт TTF/OTF не читается: %w", err)
	}
	return &ttf{f: f}, nil
}

func (t *ttf) has(r rune) bool {
	i, err := t.f.GlyphIndex(&t.buf, r)
	return err == nil && i != 0
}

// inkHeight — высота буквы в долях em.
func (t *ttf) inkHeight(r rune) float64 {
	i, _ := t.f.GlyphIndex(&t.buf, r)
	b, _, err := t.f.GlyphBounds(&t.buf, i, fixed.I(1000), font.HintingNone)
	if err != nil {
		return 0
	}
	return float64(b.Max.Y-b.Min.Y) / 64 / 1000
}

// rendered — новая буква: картинка от пера (слева) и базовой линии.
type rendered struct {
	alpha   []float64 // w×h, 0..255
	w, h    int
	x0, y0  int     // где левый верхний пиксель относительно пера и базовой линии
	advance float64 // шаг пера в пикселях, с учётом утолщения и обводки
}

// render рисует букву: ppem — размер em в пикселях, sx — сжатие по ширине,
// grow — на сколько пикселей утолщить (меньше нуля — утоньшить), st — яркость по глубине штриха.
func (t *ttf) render(r rune, ppem, sx, grow float64, st *style) (*rendered, error) {
	gi, err := t.f.GlyphIndex(&t.buf, r)
	if err != nil || gi == 0 {
		return nil, fmt.Errorf("в шрифте нет буквы %q", r)
	}
	segs, err := t.f.LoadGlyph(&t.buf, gi, fixed.Int26_6(ppem*64*ss), nil)
	if err != nil {
		return nil, err
	}
	adv, err := t.f.GlyphAdvance(&t.buf, gi, fixed.Int26_6(ppem*64), font.HintingNone)
	if err != nil {
		return nil, err
	}
	// границы контура в точках сглаживания
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	pt := func(p fixed.Point26_6) (float64, float64) {
		return float64(p.X) / 64 * sx, float64(p.Y) / 64
	}
	for _, s := range segs {
		n := map[sfnt.SegmentOp]int{sfnt.SegmentOpMoveTo: 1, sfnt.SegmentOpLineTo: 1, sfnt.SegmentOpQuadTo: 2, sfnt.SegmentOpCubeTo: 3}[s.Op]
		for _, p := range s.Args[:n] {
			x, y := pt(p)
			minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	margin := int(math.Ceil(math.Max(grow, 0))) + 2
	if math.IsInf(minX, 1) { // пустая буква (пробел)
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}
	// сетка выходных пикселей: от пера и базовой линии, кратно ss
	px0 := int(math.Floor(minX/ss)) - margin
	py0 := int(math.Floor(minY/ss)) - margin
	w := int(math.Ceil(maxX/ss)) + margin - px0
	h := int(math.Ceil(maxY/ss)) + margin - py0
	W, H := w*ss, h*ss

	ras := vector.NewRasterizer(W, H)
	ox, oy := float32(-px0*ss), float32(-py0*ss)
	at := func(p fixed.Point26_6) (float32, float32) {
		x, y := pt(p)
		return float32(x) + ox, float32(y) + oy
	}
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			ras.MoveTo(at(s.Args[0]))
		case sfnt.SegmentOpLineTo:
			ras.LineTo(at(s.Args[0]))
		case sfnt.SegmentOpQuadTo:
			x1, y1 := at(s.Args[0])
			x2, y2 := at(s.Args[1])
			ras.QuadTo(x1, y1, x2, y2)
		case sfnt.SegmentOpCubeTo:
			x1, y1 := at(s.Args[0])
			x2, y2 := at(s.Args[1])
			x3, y3 := at(s.Args[2])
			ras.CubeTo(x1, y1, x2, y2, x3, y3)
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, W, H))
	ras.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})

	// расстояние до края буквы (в выходных пикселях): снаружи — плюс, внутри — минус
	in := make([]bool, W*H)
	for i, a := range mask.Pix {
		in[i] = a >= 128
	}
	sd := signedDistance(in, W, H)
	out := &rendered{w: w, h: h, x0: px0, y0: py0, alpha: make([]float64, w*h)}
	out.advance = float64(adv)/64*sx + 2*grow
	for y := range h {
		for x := range w {
			sum := 0.0
			for dy := range ss {
				for dx := range ss {
					if d := sd[(y*ss+dy)*W+x*ss+dx] / ss; d <= grow {
						sum += st.at(grow - d)
					}
				}
			}
			out.alpha[y*w+x] = sum / ss / ss
		}
	}
	return out, nil
}

// ink — столбцы и строки, где что-то нарисовано.
func (r *rendered) ink() image.Rectangle {
	b := image.Rectangle{}
	for y := range r.h {
		for x := range r.w {
			if r.alpha[y*r.w+x] >= 1 {
				b = b.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return b
}

// signedDistance — евклидово расстояние до края фигуры для каждой точки (снаружи плюс, внутри минус).
func signedDistance(in []bool, w, h int) []float64 {
	outside := edt(in, w, h, true)
	inside := edt(in, w, h, false)
	sd := make([]float64, w*h)
	for i := range sd {
		if in[i] {
			sd[i] = -math.Sqrt(inside[i]) + 0.5
		} else {
			sd[i] = math.Sqrt(outside[i]) - 0.5
		}
	}
	return sd
}

// edt — квадрат расстояния до ближайшей точки, где in == target (Felzenszwalb, Huttenlocher).
func edt(in []bool, w, h int, target bool) []float64 {
	const inf = 1e20
	d := make([]float64, w*h)
	for i, v := range in {
		if v == target {
			d[i] = 0
		} else {
			d[i] = inf
		}
	}
	n := max(w, h)
	f, z := make([]float64, n), make([]float64, n+1)
	v, out := make([]int, n), make([]float64, n)
	pass := func(get func(int) float64, set func(int, float64), m int) {
		for i := range m {
			f[i] = get(i)
		}
		k := 0
		v[0], z[0], z[1] = 0, -inf, inf
		for q := 1; q < m; q++ {
			s := ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / float64(2*q-2*v[k])
			for s <= z[k] {
				k--
				s = ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / float64(2*q-2*v[k])
			}
			k++
			v[k], z[k], z[k+1] = q, s, inf
		}
		k = 0
		for q := range m {
			for z[k+1] < float64(q) {
				k++
			}
			out[q] = float64((q-v[k])*(q-v[k])) + f[v[k]]
		}
		for i := range m {
			set(i, out[i])
		}
	}
	for x := range w {
		pass(func(i int) float64 { return d[i*w+x] }, func(i int, val float64) { d[i*w+x] = val }, h)
	}
	for y := range h {
		pass(func(i int) float64 { return d[y*w+i] }, func(i int, val float64) { d[y*w+i] = val }, w)
	}
	return d
}
