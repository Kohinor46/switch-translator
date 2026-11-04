package codec

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Блок LZ4 со ссылкой, перекрывающей дописываемое: «abc» + 9 повторов с шагом 3.
func TestLZ4Block(t *testing.T) {
	frame := append([]byte(nil), lz4Magic...)
	frame = append(frame, 0x60, 0x70, 0x00) // FLG, BD, контрольная сумма заголовка (не проверяется)
	block := []byte{0x35, 'a', 'b', 'c', 3, 0, 0x10, 'z'}
	frame = binary.LittleEndian.AppendUint32(frame, uint32(len(block)))
	frame = append(frame, block...)
	frame = append(frame, 0, 0, 0, 0)
	got, err := LZ4(frame, -1)
	if err != nil || string(got) != "abcabcabcabcz" {
		t.Fatalf("%q, %v", got, err)
	}
	sized := binary.LittleEndian.AppendUint32(nil, uint32(len(got)))
	if out, chain, err := Unwrap(append(sized, frame...)); err != nil || chain != "lz4s" || !bytes.Equal(out, got) {
		t.Fatalf("с размером впереди: %q %s %v", out, chain, err)
	}
}

// Упакованное снова распаковывается; контрольная сумма заголовка — xxHash32 (эталон: xxh32("") = 02CC5D05, xxh32("abc") = 32D153FF).
func TestLZ4Frame(t *testing.T) {
	if xxh32(nil) != 0x02CC5D05 || xxh32([]byte("abc")) != 0x32D153FF {
		t.Fatalf("xxh32: %08X %08X", xxh32(nil), xxh32([]byte("abc")))
	}
	if h := xxh32([]byte("Nobody inspects the spammish repetition")); h != 0xE2293B2F {
		t.Fatalf("xxh32 длинной строки: %08X", h)
	}
	data := bytes.Repeat([]byte("звук "), 2_000_000) // больше одного блока
	for _, sized := range []bool{false, true} {
		got, err := LZ4(LZ4Frame(data, sized), -1)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("sized=%v: %v", sized, err)
		}
	}
	if w, _ := Wrap([]byte("abc"), "lz4s", nil); Wrapper(w) != "lz4s" {
		t.Fatalf("Wrap lz4s: %q", w[:8])
	}
}

// Настоящие файлы: SWTR_LZ4=<папка с *.cmpbin> (Kirby and the Forgotten Land) — внутри должен оказаться BFSAR нужного размера.
func TestLZ4Real(t *testing.T) {
	dir := os.Getenv("SWTR_LZ4")
	if dir == "" {
		t.Skip("SWTR_LZ4 не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.cmpbin"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		out, chain, err := Unwrap(b)
		if err != nil || chain != "lz4s" {
			t.Fatalf("%s: %v (%s)", filepath.Base(f), err, chain)
		}
		if !bytes.HasPrefix(out, []byte("FSAR")) || len(out) != int(binary.LittleEndian.Uint32(b)) {
			t.Fatalf("%s: распаковано %d байт, начало %q", filepath.Base(f), len(out), out[:4])
		}
		head, _ := LZ4(b[:min(len(b), 4096)], 64)
		if !bytes.Equal(head, out[:64]) {
			t.Fatalf("%s: подглядывание не совпало", filepath.Base(f))
		}
	}
	t.Logf("распаковано %d файлов", len(files))
}
