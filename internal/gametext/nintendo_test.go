package gametext

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"switch-translator/internal/bffnt"
	"switch-translator/internal/codec"
)

func utf16le(s string) []byte {
	var b []byte
	for _, c := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

func section(magic string, body []byte) []byte {
	out := []byte(magic)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	out = append(out, make([]byte, 8)...)
	out = append(out, body...)
	for len(out)%16 != 0 {
		out = append(out, 0xAB)
	}
	return out
}

func buildMSBT() []byte {
	var lbl []byte
	lbl = binary.LittleEndian.AppendUint32(lbl, 1)
	lbl = binary.LittleEndian.AppendUint32(lbl, 2)
	lbl = binary.LittleEndian.AppendUint32(lbl, 12)
	for i, name := range []string{"Talk_000", "Talk_001"} {
		lbl = append(lbl, byte(len(name)))
		lbl = append(lbl, name...)
		lbl = binary.LittleEndian.AppendUint32(lbl, uint32(i))
	}

	s0 := append(utf16le("こんにちは、"), 0x0E, 0, 1, 0, 2, 0, 2, 0, 0xAA, 0xBB)
	s0 = append(s0, utf16le("リンク")...)
	s0 = append(s0, 0x0F, 0, 1, 0, 2, 0)
	s0 = append(s0, 0, 0)
	s1 := append(utf16le("さようなら"), 0, 0)
	var txt []byte
	txt = binary.LittleEndian.AppendUint32(txt, 2)
	txt = binary.LittleEndian.AppendUint32(txt, 12)
	txt = binary.LittleEndian.AppendUint32(txt, uint32(12+len(s0)))
	txt = append(append(txt, s0...), s1...)

	h := []byte("MsgStdBn")
	h = append(h, 0xFF, 0xFE, 0, 0, 1, 3)
	h = binary.LittleEndian.AppendUint16(h, 2)
	h = append(h, make([]byte, 0x20-len(h))...)
	return append(append(h, section("LBL1", lbl)...), section("TXT2", txt)...)
}

func buildSARC(name string, data []byte) []byte {
	le := binary.LittleEndian
	nameBlock := append([]byte(name), 0)
	for len(nameBlock)%4 != 0 {
		nameBlock = append(nameBlock, 0)
	}
	hdrLen := 0x14
	sfat := []byte("SFAT")
	sfat = le.AppendUint16(sfat, 0xC)
	sfat = le.AppendUint16(sfat, 1)
	sfat = le.AppendUint32(sfat, 0x65)
	sfat = le.AppendUint32(sfat, 0)
	sfat = le.AppendUint32(sfat, 0x01000000)
	sfat = le.AppendUint32(sfat, 0)
	sfat = le.AppendUint32(sfat, uint32(len(data)))
	sfnt := append([]byte("SFNT"), 8, 0, 0, 0)
	sfnt = append(sfnt, nameBlock...)
	dataOff := hdrLen + len(sfat) + len(sfnt)
	h := []byte("SARC")
	h = le.AppendUint16(h, uint16(hdrLen))
	h = append(h, 0xFF, 0xFE)
	h = le.AppendUint32(h, uint32(dataOff+len(data)))
	h = le.AppendUint32(h, uint32(dataOff))
	h = le.AppendUint16(h, 0x100)
	h = le.AppendUint16(h, 0)
	return append(append(append(h, sfat...), sfnt...), data...)
}

func yaz0Literal(raw []byte) []byte {
	out := []byte("Yaz0")
	out = binary.BigEndian.AppendUint32(out, uint32(len(raw)))
	out = append(out, make([]byte, 8)...)
	for i := 0; i < len(raw); i += 8 {
		out = append(out, 0xFF)
		chunk := make([]byte, 8)
		copy(chunk, raw[i:])
		out = append(out, chunk...)
	}
	return out
}

func TestExtractFromYaz0SARC(t *testing.T) {
	szs := yaz0Literal(buildSARC("Npc/Talk.msbt", buildMSBT()))
	got, err := Extract("Message/Msg_JPja.sarc.szs", szs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("строк: %d, ждали 2", len(got))
	}
	want0 := "こんにちは、{{1:2:aabb}}リンク{{/1:2}}"
	if got[0].Source != want0 || got[0].Label != "Talk_000" {
		t.Errorf("строка 0: %q (%s), ждали %q", got[0].Source, got[0].Label, want0)
	}
	if got[1].Source != "さようなら" || got[1].ID != "Message/Msg_JPja.sarc.szs/Npc/Talk.msbt#Talk_001" {
		t.Errorf("строка 1: %+v", got[1])
	}
}

// TestWriteSZS пишет тестовый Yaz0+SARC+MSBT в SWTR_MSBT (для TestWriteFixture в пакете nx).
func TestWriteSZS(t *testing.T) {
	p := os.Getenv("SWTR_MSBT")
	if p == "" {
		t.Skip("SWTR_MSBT не задан")
	}
	if err := os.WriteFile(p, yaz0Literal(buildSARC("Npc/Talk.msbt", buildMSBT())), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocale(t *testing.T) {
	for in, want := range map[string]string{
		"LocalizedData/EUde/MessageData/SystemMessage.szs": "EUde",
		"Mals/USen.Product.100.sarc.zs":                    "USen",
		"Message/Msg_JPja.sarc.szs":                        "JPja",
		"Message/Common.szs":                               "",
		"Font/USA/font.bfttf":                              "",
		"LocalizedData/EuDe/MessageData/LayoutMessage.szs": "EUde",
		"msg/EU_Dutch/aji.msbt":                            "EUnl",
		"UsEnglish/MessageData/Message.arc":                "USen",
		"AsTradChinese/MessageData/Message.arc":            "TWzh",
		"CnSimpChinese/MessageData/Message.arc":            "CNzh",
		"LuigiBros/msg/EUR_de/SYS.msbt":                    "EUde",
		"LuigiBros/msg/JPN_ja/SYS.msbt":                    "JPja",
		"msg/US_English_Final/aji.msbt":                    "USen",
		"msg/Kirby15/CN_Chinese/Arena.msbt":                "CNzh",
		"Mals/THth.Product.120.sarc.zs":                    "THth",
		"Sound/Euro/Bgm.bars":                              "",
		"Data/US_Map/x.bin":                                "",
	} {
		if got := Locale(in); got != want {
			t.Errorf("Locale(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestYaz0RoundTrip(t *testing.T) {
	var in []byte
	for i := range 3000 {
		in = append(in, []byte("Press the button to jump! ")...)
		in = append(in, byte(i), byte(i>>8))
	}
	enc := codec.Yaz0Encode(in)
	dec, err := codec.Yaz0(enc, -1)
	if err != nil || !bytes.Equal(dec, in) {
		t.Fatalf("Yaz0 не совпал: %v", err)
	}
	if len(enc) > len(in)/2 {
		t.Errorf("Yaz0 почти не сжал: %d → %d", len(in), len(enc))
	}
}

// TestApplyRealGame — вписывает перевод во все файлы с текстом и читает их заново.
// SWTR_TEXT_ROMFS=<папка romfs> go test ./internal/gametext -run ApplyReal -v
func TestApplyRealGame(t *testing.T) {
	root := os.Getenv("SWTR_TEXT_ROMFS")
	if root == "" {
		t.Skip("SWTR_TEXT_ROMFS не задан")
	}
	files := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		strs, err := Extract(rel, data)
		if err != nil || len(strs) == 0 {
			return nil
		}
		tr := map[string]string{}
		for _, s := range strs {
			tr[s.ID] = "Привет " + s.Source
		}
		out, n, err := Apply(rel, data, tr)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if n != len(strs) {
			t.Fatalf("%s: вписано %d из %d", rel, n, len(strs))
		}
		back, err := Extract(rel, out)
		if err != nil {
			t.Fatalf("%s: повторное чтение: %v", rel, err)
		}
		if len(back) != len(strs) {
			t.Fatalf("%s: после записи %d строк вместо %d", rel, len(back), len(strs))
		}
		for i := range back {
			if back[i].Source != tr[strs[i].ID] {
				t.Fatalf("%s: %s = %q, ждали %q", rel, back[i].ID, back[i].Source, tr[strs[i].ID])
			}
		}
		// без перевода файл не меняется
		if same, n, _ := Apply(rel, data, nil); same != nil || n != 0 {
			t.Fatalf("%s: без перевода файл изменился", rel)
		}
		if codec.Wrapper(data) != "" {
			t.Logf("%s: было %d байт, стало %d", rel, len(data), len(out))
		}
		files++
		return nil
	})
	if files == 0 {
		t.Fatal("не найдено ни одного файла с текстом")
	}
	t.Logf("проверено %d файлов", files)
}

// SWTR_FONT=<FontData.szs> go test ./internal/gametext -run FontReal -v
func TestFontReal(t *testing.T) {
	for _, p := range filepath.SplitList(os.Getenv("SWTR_FONT")) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fonts, err := Fonts(filepath.Base(p), data)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fonts {
			t.Logf("%s: %d символов, нет для «Привет, Жёлтый!»: %q", f.Name, len(f.Chars), string(f.Missing("Привет, Жёлтый!")))
		}
	}
}

// Векторные шрифты Switch (BFTTF/BFOTF) зашифрованы XOR — их буквы тоже должны находиться,
// иначе сборка жалуется на «нет символов», хотя игра рисует кириллицу сама.
func TestScalableFont(t *testing.T) {
	ttf := bffnt.DefaultTTF()
	enc := make([]byte, 8+len(ttf)+3)
	binary.BigEndian.PutUint32(enc, 0x7F9A0218)
	binary.BigEndian.PutUint32(enc[4:], uint32(len(ttf)))
	copy(enc[8:], ttf)
	for i := range enc {
		enc[i] ^= bfttfKey[i%4]
	}
	fonts, err := Fonts("ScalableFont.szs", buildSARC("Font/Text.bfttf", enc))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Fonts("Plain.ttf", ttf)
	if err != nil {
		t.Fatal(err)
	}
	if fonts = append(fonts, plain...); len(fonts) != 2 {
		t.Fatalf("шрифтов %d", len(fonts))
	}
	for _, f := range fonts {
		if !f.Scalable || len(f.Missing("Привет, Жёлтый! 10…")) != 0 {
			t.Errorf("%s: scalable=%v, нет %q", f.Name, f.Scalable, string(f.Missing("Привет, Жёлтый! 10…")))
		}
	}
}

// Настоящие архивы: SWTR_RARC=<папка> (Super Mario Galaxy). Каждое сообщение BMG кодируется обратно
// в те же байты, а архив с переписанным BMG разбирается и даёт новые тексты.
func TestRARCReal(t *testing.T) {
	dir := os.Getenv("SWTR_RARC")
	if dir == "" {
		t.Skip("SWTR_RARC не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "MessageData", "*.arc"))
	total := 0
	for _, p := range files {
		b, _ := os.ReadFile(p)
		file := strings.TrimPrefix(p, dir+"/")
		strs, err := Extract(file, b)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		raw, _, _ := codec.Unwrap(b)
		a, _ := parseRARC(raw)
		for _, f := range a.files {
			if !isBMG(f.Data) {
				continue
			}
			m, _ := parseBMG(f.Data)
			for i, off := range m.offsets {
				if off == 0 {
					continue
				}
				enc, err := m.encode(m.text(i))
				orig := f.Data[m.dat+8+off:]
				if err != nil || !bytes.HasPrefix(orig, enc) {
					t.Fatalf("%s/%s #%d: %q не кодируется обратно: %v", file, f.Name, i, m.text(i), err)
				}
			}
		}
		total += len(strs)
		if len(strs) == 0 {
			continue
		}
		tr := map[string]string{}
		for i, s := range strs {
			if i%3 == 0 {
				tr[s.ID] = "Перевод «" + s.Source + "»"
			}
		}
		out, n, err := Apply(file, b, tr)
		if err != nil || n != len(tr) {
			t.Fatalf("%s: вписано %d из %d: %v", file, n, len(tr), err)
		}
		again, err := Extract(file, out)
		if err != nil || len(again) != len(strs) {
			t.Fatalf("%s: после сборки %d строк из %d: %v", file, len(again), len(strs), err)
		}
		for i, s := range again {
			want := strs[i].Source
			if w, ok := tr[s.ID]; ok {
				want = w
			}
			if s.ID != strs[i].ID || s.Source != want {
				t.Fatalf("%s: %q, ждали %q", s.ID, s.Source, want)
			}
		}
	}
	t.Logf("архивов %d, строк %d", len(files), total)
}
