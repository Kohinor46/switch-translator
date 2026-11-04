package unreal

import (
	"bytes"
	"testing"
)

// Заплатка собирается и читается обратно; обрезанный архив даёт ошибку, а не панику.
func TestPakRoundTrip(t *testing.T) {
	files := map[string][]byte{
		"Game/Content/Localization/Game/ru/Game.locres": bytes.Repeat([]byte("привет"), 100),
		"Game/Content/Localization/Game/ru/Game.txt":    []byte("x"),
	}
	names := []string{"Game/Content/Localization/Game/ru/Game.locres", "Game/Content/Localization/Game/ru/Game.txt"}
	b := WritePak("../../../", names, files)

	p, err := OpenPak(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("записей %d", len(p.Entries))
	}
	for _, e := range p.Entries {
		got, err := p.Read(e)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		want := files[e.Name[len("../../../"):]]
		if !bytes.Equal(got, want) {
			t.Errorf("%s: прочитано %d байт вместо %d", e.Name, len(got), len(want))
		}
	}

	// запись, у которой данные выходят за конец файла
	e := p.Entries[0]
	e.RawSize = int64(len(b))
	if _, err := p.Read(e); err == nil {
		t.Error("чтение за концом файла прошло без ошибки")
	}
	e = p.Entries[0]
	e.RawSize = -1
	if _, err := p.Read(e); err == nil {
		t.Error("отрицательный размер прошёл без ошибки")
	}
	// сжатая запись без блоков в архиве версии 3
	e = p.Entries[0]
	e.Compression = 1
	if _, err := p.Read(e); err == nil {
		t.Error("сжатая запись без блоков прошла без ошибки")
	}
	// блоки с перепутанными границами
	e.Blocks = [][2]int64{{100, 10}}
	if _, err := p.Read(e); err == nil {
		t.Error("блок с концом раньше начала прошёл без ошибки")
	}
}
