package nx

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"switch-translator/internal/keys"
)

// Тесты собирают NSP/NSZ «с нуля» на случайных ключах и проверяют, что
// распаковщик проходит весь путь до файлов RomFS. Совместимость с настоящими
// играми так не проверить — только внутреннюю согласованность формата.

type testFile struct {
	name string
	data []byte
}

func buildRomFS(subDir string, subFile testFile, root testFile) []byte {
	pad4 := func(n int) int { return (n + 3) &^ 3 }
	le32 := func(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
	le64 := func(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

	subDirOff := uint32(0x18)
	var dirs []byte
	dirs = le32(dirs, 0)
	dirs = le32(dirs, romfsNone)
	dirs = le32(dirs, subDirOff)
	dirs = le32(dirs, 0) // файл root.name
	dirs = le32(dirs, 0)
	dirs = le32(dirs, 0)
	fileBOff := uint32(0x20 + pad4(len(root.name)))
	dirs = le32(dirs, 0)
	dirs = le32(dirs, romfsNone)
	dirs = le32(dirs, romfsNone)
	dirs = le32(dirs, fileBOff)
	dirs = le32(dirs, 0)
	dirs = le32(dirs, uint32(len(subDir)))
	dirs = append(dirs, make([]byte, pad4(len(subDir)))...)
	copy(dirs[0x30:], subDir)

	bDataOff := uint64((len(root.data) + 15) &^ 15)
	var files []byte
	for _, f := range []struct {
		parent uint32
		tf     testFile
		off    uint64
	}{{0, root, 0}, {subDirOff, subFile, bDataOff}} {
		start := len(files)
		files = le32(files, f.parent)
		files = le32(files, romfsNone)
		files = le64(files, f.off)
		files = le64(files, uint64(len(f.tf.data)))
		files = le32(files, 0)
		files = le32(files, uint32(len(f.tf.name)))
		files = append(files, make([]byte, pad4(len(f.tf.name)))...)
		copy(files[start+0x20:], f.tf.name)
	}

	dirMetaOff := uint64(0x54)
	fileHashOff := dirMetaOff + uint64(len(dirs))
	fileMetaOff := fileHashOff + 4
	dataOff := (fileMetaOff + uint64(len(files)) + 15) &^ 15

	var h []byte
	h = le64(h, 0x50)
	h = le64(h, 0x50)
	h = le64(h, 4)
	h = le64(h, dirMetaOff)
	h = le64(h, uint64(len(dirs)))
	h = le64(h, fileHashOff)
	h = le64(h, 4)
	h = le64(h, fileMetaOff)
	h = le64(h, uint64(len(files)))
	h = le64(h, dataOff)
	out := append(h, make([]byte, 4)...)
	out = append(out, dirs...)
	out = append(out, make([]byte, 4)...)
	out = append(out, files...)
	out = append(out, make([]byte, int(dataOff)-len(out))...)
	out = append(out, root.data...)
	out = append(out, make([]byte, int(bDataOff)-len(root.data))...)
	out = append(out, subFile.data...)
	return out
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

type fixture struct {
	keysPath string
	ks       *keys.Keyset
	nca      []byte // зашифрованный NCA
	plain    []byte // тот же NCA с расшифрованным телом (как внутри NCZ)
	files    map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, append([]byte("MsgStdBn"), randBytes(200)...))
}

func newFixtureWith(t *testing.T, msbt []byte) *fixture {
	t.Helper()
	dir := t.TempDir()
	headerKey, kak, ctrKey := randBytes(32), randBytes(16), randBytes(16)
	keysPath := filepath.Join(dir, "prod.keys")
	os.WriteFile(keysPath, []byte(fmt.Sprintf("header_key = %s\nkey_area_key_application_00 = %s\ntitlekek_00 = %s\n",
		hex.EncodeToString(headerKey), hex.EncodeToString(kak), hex.EncodeToString(randBytes(16)))), 0o600)
	ks, err := keys.Load(keysPath)
	if err != nil {
		t.Fatal(err)
	}

	bfstm := append([]byte("FSTM"), randBytes(70000)...)
	romfs := buildRomFS("Sound", testFile{"bgm.bfstm", bfstm}, testFile{"Talk.msbt", msbt})

	const secOff = 0x4000
	secData := append(make([]byte, 0x200), romfs...)
	secData = append(secData, make([]byte, (0x200-len(secData)%0x200)%0x200)...)

	hdr := make([]byte, ncaHeaderSize)
	copy(hdr[0x200:], "NCA3")
	hdr[0x205] = byte(ContentProgram)
	binary.LittleEndian.PutUint64(hdr[0x210:], 0x0100ABCD00000000)
	binary.LittleEndian.PutUint32(hdr[0x240:], secOff/0x200)
	binary.LittleEndian.PutUint32(hdr[0x244:], uint32((secOff+len(secData))/0x200))
	area := make([]byte, 0x40)
	copy(area[0x20:], ctrKey)
	copy(hdr[0x300:], ecbEncrypt(kak, area))
	fh := hdr[0x400:0x600]
	fh[2], fh[3], fh[4] = fsRomFS, 3, encCTR
	copy(fh[0x8:], "IVFC")
	binary.LittleEndian.PutUint32(fh[0x8+0xC:], 7)
	lvl := fh[0x8+0x10+5*0x18:]
	binary.LittleEndian.PutUint64(lvl[0:], 0x200)
	binary.LittleEndian.PutUint64(lvl[8:], uint64(len(romfs)))
	ctr := randBytes(8)
	copy(fh[0x140:], ctr)
	var secCtr [8]byte
	for j := 0; j < 8; j++ {
		secCtr[j] = ctr[7-j]
	}

	plain := make([]byte, secOff+len(secData))
	copy(plain, hdr)
	newXTS(headerKey).crypt(plain[:ncaHeaderSize], 0, 0x200, false)
	copy(plain[secOff:], secData)

	enc := append([]byte(nil), plain...)
	block, _ := aes.NewCipher(ctrKey)
	cipher.NewCTR(block, ctrIV(secCtr, secOff)).XORKeyStream(enc[secOff:], enc[secOff:])

	return &fixture{keysPath: keysPath, ks: ks, nca: enc, plain: plain,
		files: map[string][]byte{"Talk.msbt": msbt, "Sound/bgm.bfstm": bfstm}}
}

func buildPFS0(files []testFile) []byte {
	var strs []byte
	var entries []byte
	var off uint64
	for _, f := range files {
		entries = binary.LittleEndian.AppendUint64(entries, off)
		entries = binary.LittleEndian.AppendUint64(entries, uint64(len(f.data)))
		entries = binary.LittleEndian.AppendUint32(entries, uint32(len(strs)))
		entries = binary.LittleEndian.AppendUint32(entries, 0)
		strs = append(append(strs, f.name...), 0)
		off += uint64(len(f.data))
	}
	for len(strs)%0x20 != 0 {
		strs = append(strs, 0)
	}
	out := []byte("PFS0")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(files)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(strs)))
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = append(append(out, entries...), strs...)
	for _, f := range files {
		out = append(out, f.data...)
	}
	return out
}

func buildNCZ(plain []byte, blockExp int) []byte {
	const off = nczHeaderSize
	out := append([]byte(nil), plain[:off]...)
	out = append(out, "NCZSECTN"...)
	out = binary.LittleEndian.AppendUint64(out, 1)
	sec := make([]byte, 0x40)
	binary.LittleEndian.PutUint64(sec[0:], off)
	binary.LittleEndian.PutUint64(sec[8:], uint64(len(plain)-off))
	binary.LittleEndian.PutUint64(sec[16:], 3)
	out = append(out, sec...)
	body := plain[off:]
	enc, _ := zstd.NewWriter(nil)
	if blockExp == 0 {
		return enc.EncodeAll(body, out)
	}
	bs := 1 << blockExp
	var blocks [][]byte
	for i := 0; i < len(body); i += bs {
		chunk := body[i:min(i+bs, len(body))]
		c := enc.EncodeAll(chunk, nil)
		if len(c) >= len(chunk) {
			c = chunk
		}
		blocks = append(blocks, c)
	}
	h := []byte("NCZBLOCK")
	h = append(h, 2, 1, 0, byte(blockExp))
	h = binary.LittleEndian.AppendUint32(h, uint32(len(blocks)))
	h = binary.LittleEndian.AppendUint64(h, uint64(len(body)))
	for _, b := range blocks {
		h = binary.LittleEndian.AppendUint32(h, uint32(len(b)))
	}
	out = append(out, h...)
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

func checkGame(t *testing.T, fx *fixture, image []byte, ext string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "game"+ext)
	os.WriteFile(path, image, 0o600)
	g, err := OpenGame(path, nil, fx.ks, dir, func(string, ...any) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.TitleID != 0x0100ABCD00000000 {
		t.Errorf("title id = %016X", g.TitleID)
	}
	if len(g.RomFS.Files) != len(fx.files) {
		t.Fatalf("файлов в RomFS: %d, ждали %d", len(g.RomFS.Files), len(fx.files))
	}
	for _, f := range g.RomFS.Files {
		want, ok := fx.files[f.Path]
		if !ok {
			t.Errorf("лишний файл %q", f.Path)
			continue
		}
		got, _ := io.ReadAll(g.RomFS.Open(f))
		if !bytes.Equal(got, want) {
			t.Errorf("%s: содержимое не совпало", f.Path)
		}
	}
	out := filepath.Join(dir, "out")
	if err := g.Extract(out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "Sound", "bgm.bfstm")); !bytes.Equal(b, fx.files["Sound/bgm.bfstm"]) {
		t.Error("извлечённый bgm.bfstm не совпал")
	}
}

func TestNSP(t *testing.T) {
	fx := newFixture(t)
	nsp := buildPFS0([]testFile{{"0123456789abcdef0123456789abcdef.nca", fx.nca}})
	checkGame(t, fx, nsp, ".nsp")
}

func TestNSZStream(t *testing.T) {
	fx := newFixture(t)
	nsz := buildPFS0([]testFile{{"0123456789abcdef0123456789abcdef.ncz", buildNCZ(fx.plain, 0)}})
	checkGame(t, fx, nsz, ".nsz")
}

func TestNSZBlock(t *testing.T) {
	fx := newFixture(t)
	nsz := buildPFS0([]testFile{{"0123456789abcdef0123456789abcdef.ncz", buildNCZ(fx.plain, 14)}})
	checkGame(t, fx, nsz, ".nsz")
}

func TestWrongHeaderKey(t *testing.T) {
	fx := newFixture(t)
	fx.ks.HeaderKey = randBytes(32)
	if _, err := OpenNCA(bytes.NewReader(fx.nca), fx.ks, nil, false); err == nil {
		t.Fatal("ждали ошибку при неверном header_key")
	}
}

// TestWriteFixture пишет тестовые prod.keys, game.nsp и game.nsz в SWTR_FIXTURE_DIR —
// чтобы прогнать программу целиком без настоящей игры. Файл Talk.msbt берётся из SWTR_MSBT.
func TestWriteFixture(t *testing.T) {
	dir := os.Getenv("SWTR_FIXTURE_DIR")
	if dir == "" {
		t.Skip("SWTR_FIXTURE_DIR не задан")
	}
	msbt, err := os.ReadFile(os.Getenv("SWTR_MSBT"))
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureWith(t, msbt)
	keysData, _ := os.ReadFile(fx.keysPath)
	os.WriteFile(filepath.Join(dir, "prod.keys"), keysData, 0o600)
	os.WriteFile(filepath.Join(dir, "game.nsp"), buildPFS0([]testFile{{"0123456789abcdef0123456789abcdef.nca", fx.nca}}), 0o644)
	os.WriteFile(filepath.Join(dir, "game.nsz"), buildPFS0([]testFile{{"0123456789abcdef0123456789abcdef.ncz", buildNCZ(fx.plain, 0)}}), 0o644)
}
