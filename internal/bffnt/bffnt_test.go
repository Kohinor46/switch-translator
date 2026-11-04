package bffnt

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"switch-translator/internal/codec"
)

// Раскладка Tegra X1 и BC4: лист, закодированный и прочитанный обратно, совпадает.
func TestLayerRoundTrip(t *testing.T) {
	pf := formats[0x1D]
	img := image.NewNRGBA(image.Rect(0, 0, 128, 256))
	for y := range 256 {
		for x := range 128 {
			a := uint8(0)
			switch {
			case (x/9+y/7)%3 == 0:
				a = 255
			case (x/9+y/7)%3 == 1:
				a = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, a})
		}
	}
	bh := blockHeightFor(256, pf)
	data := encodeLayer(img, pf, bh)
	if len(data) != layerSize(128, 256, pf, bh) {
		t.Fatalf("размер листа %d", len(data))
	}
	back := decodeLayer(data, 128, 256, pf, bh)
	for i := 3; i < len(img.Pix); i += 4 {
		if d := int(img.Pix[i]) - int(back.Pix[i]); d > 2 || d < -2 {
			t.Fatalf("пиксель %d: %d против %d", i/4, img.Pix[i], back.Pix[i])
		}
	}
}

// Настоящие шрифты: SWTR_FONT=<FontData.szs>. Дописываем кириллицу, старые буквы на месте.
func TestAddReal(t *testing.T) {
	p := os.Getenv("SWTR_FONT")
	if p == "" {
		t.Skip("SWTR_FONT не задан")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := codec.Unwrap(data)
	for _, name := range []string{"MessageFont38.bffnt", "TitleFont32.bffnt", "MarioFont128.bffnt"} {
		i := bytes.Index(raw, []byte(name))
		if i < 0 {
			continue
		}
		font := findFont(t, raw, name)
		f, err := Parse(font)
		if err != nil {
			t.Fatal(err)
		}
		res, err := f.Add(Cyrillic, Options{TTF: DefaultTTF(), Reuse: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g, err := Parse(res.Data)
		if err != nil {
			t.Fatalf("%s: новый шрифт не читается: %v", name, err)
		}
		for c, i := range f.Chars() {
			if g.Index(c) != i {
				t.Errorf("%s: буква %q сломалась", name, c)
			}
		}
		for _, c := range Cyrillic {
			if g.Index(c) < 0 && !bytes.ContainsRune([]byte(string(res.Missing)), c) {
				t.Errorf("%s: нет буквы %q", name, c)
			}
		}
		t.Logf("%s: нарисовано %d, из игры %d, листов +%d", name, len(res.Drawn), len(res.Reused), res.Sheets)
		if out := os.Getenv("SWTR_OUT"); out != "" {
			f, _ := os.Create(filepath.Join(out, name+".png"))
			encodePNG(f, g.Render("Съешь же ещё этих мягких французских булок, да выпей чаю!"))
			f.Close()
		}
	}
}

func encodePNG(w *os.File, img image.Image) { png.Encode(w, img) }

// findFont достаёт шрифт из SARC по имени.
func findFont(t *testing.T, raw []byte, want string) []byte {
	hdr := int(le.Uint16(raw[4:]))
	dataOff := int(le.Uint32(raw[0xC:]))
	count := int(le.Uint16(raw[hdr+6:]))
	names := hdr + 0xC + count*0x10 + 8
	for i := range count {
		n := hdr + 0xC + i*0x10
		name := raw[names+int(le.Uint32(raw[n+4:])&0xFFFFFF)*4:]
		name = name[:bytes.IndexByte(name, 0)]
		if string(name) != want {
			continue
		}
		d, _, err := codec.Unwrap(raw[dataOff+int(le.Uint32(raw[n+8:])) : dataOff+int(le.Uint32(raw[n+12:]))])
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Fatalf("нет %s", want)
	return nil
}
