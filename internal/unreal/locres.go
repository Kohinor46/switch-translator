package unreal

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// .locres — переводы текстов игры на один язык: пространства имён → ключи → строки.
// SourceHash — хэш исходной строки из ассета; движок показывает перевод, только если хэш совпал,
// поэтому он переписывается как был.

var locresMagic = []byte{0x0E, 0x14, 0x74, 0x75, 0x67, 0x4A, 0x03, 0xFC, 0x4A, 0x15, 0x90, 0x9D, 0xC3, 0x37, 0x7F, 0x1B}

var locmetaMagic = []byte{0x4F, 0xEE, 0x4C, 0xA1, 0x68, 0x48, 0x55, 0x83, 0x6C, 0x4C, 0x46, 0xBD, 0x70, 0xDA, 0x50, 0x7C}

// LocMetaNative — исходный язык из .locmeta (рядом с папками языков): его тексты лежат
// в самих ассетах, а .locres остальных языков содержат только то, что от них отличается.
func LocMetaNative(b []byte) (string, error) {
	if !bytes.HasPrefix(b, locmetaMagic) || len(b) < 17 {
		return "", fmt.Errorf(".locmeta: нет подписи")
	}
	r := &reader{b: b, p: 17}
	s := r.fstring()
	return s, r.err
}

// LocText — строка .locres.
type LocText struct {
	Namespace, Key  string
	NsHash, KeyHash uint32 // только в версиях 2+
	SourceHash      uint32
	Text            string
}

// Locres — разобранный файл.
type Locres struct {
	Version byte // 0 — старый формат без подписи
	Texts   []LocText
}

// ParseLocres разбирает .locres.
func ParseLocres(b []byte) (*Locres, error) {
	r := &reader{b: b}
	l := &Locres{}
	var strs []string
	if bytes.HasPrefix(b, locresMagic) {
		r.p = 16
		l.Version = r.u8()
		if l.Version > 3 {
			return nil, fmt.Errorf(".locres версии %d программа не знает", l.Version)
		}
		arr := r.i64()
		if arr < 0 || arr > int64(len(b)) {
			return nil, fmt.Errorf(".locres: таблица строк за концом файла")
		}
		sr := &reader{b: b, p: int(arr)}
		n := int(sr.i32())
		if n < 0 || n > 1<<22 {
			return nil, fmt.Errorf(".locres: %d строк", n)
		}
		for range n {
			strs = append(strs, sr.fstring())
			if l.Version >= 2 {
				sr.i32() // сколько ключей ссылается
			}
		}
		if sr.err != nil {
			return nil, sr.err
		}
		if l.Version >= 2 {
			r.u32() // всего записей
		}
	}
	nsCount := int(r.u32())
	for range nsCount {
		var ns LocText
		if l.Version >= 2 {
			ns.NsHash = r.u32()
		}
		ns.Namespace = r.fstring()
		keys := int(r.u32())
		if r.err != nil || keys < 0 || keys > 1<<22 {
			return nil, fmt.Errorf(".locres: испорчено пространство %q", ns.Namespace)
		}
		for range keys {
			t := ns
			if l.Version >= 2 {
				t.KeyHash = r.u32()
			}
			t.Key = r.fstring()
			t.SourceHash = r.u32()
			if l.Version >= 1 {
				i := int(r.i32())
				if i < 0 || i >= len(strs) {
					return nil, fmt.Errorf(".locres: ссылка на строку %d из %d", i, len(strs))
				}
				t.Text = strs[i]
			} else {
				t.Text = r.fstring()
			}
			l.Texts = append(l.Texts, t)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return l, nil
}

// Bytes — файл .locres в той же версии; одинаковые строки хранятся один раз.
func (l *Locres) Bytes() []byte {
	var w writer
	var header writer
	type ns struct {
		name string
		hash uint32
		keys []int
	}
	var order []*ns
	byName := map[string]*ns{}
	for i, t := range l.Texts {
		n := byName[t.Namespace]
		if n == nil {
			n = &ns{name: t.Namespace, hash: t.NsHash}
			byName[t.Namespace] = n
			order = append(order, n)
		}
		n.keys = append(n.keys, i)
	}
	index := map[string]int{}
	var strs []string
	refs := map[int]int{}
	if l.Version >= 2 {
		w.u32(uint32(len(l.Texts)))
	}
	w.u32(uint32(len(order)))
	for _, n := range order {
		if l.Version >= 2 {
			w.u32(n.hash)
		}
		w.fstring(n.name)
		w.u32(uint32(len(n.keys)))
		for _, i := range n.keys {
			t := l.Texts[i]
			if l.Version >= 2 {
				w.u32(t.KeyHash)
			}
			w.fstring(t.Key)
			w.u32(t.SourceHash)
			if l.Version >= 1 {
				k, ok := index[t.Text]
				if !ok {
					k = len(strs)
					index[t.Text] = k
					strs = append(strs, t.Text)
				}
				refs[k]++
				w.u32(uint32(k))
			} else {
				w.fstring(t.Text)
			}
		}
	}
	if l.Version == 0 {
		return w.b
	}
	header.b = append(header.b, locresMagic...)
	header.b = append(header.b, l.Version)
	arrOff := len(header.b) + 8 + len(w.b)
	header.u64(uint64(arrOff))
	out := append(header.b, w.b...)
	var sa writer
	sa.u32(uint32(len(strs)))
	for k, s := range strs {
		sa.fstring(s)
		if l.Version >= 2 {
			sa.u32(uint32(refs[k]))
		}
	}
	return append(out, sa.b...)
}

type writer struct{ b []byte }

func (w *writer) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *writer) u64(v uint64) { w.b = binary.LittleEndian.AppendUint64(w.b, v) }

// fstring — строка UE: ASCII — байтами с нулём, иначе UTF-16 с отрицательной длиной.
func (w *writer) fstring(s string) {
	if s == "" {
		w.u32(0)
		return
	}
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		w.u32(uint32(len(s) + 1))
		w.b = append(w.b, s...)
		w.b = append(w.b, 0)
		return
	}
	u := utf16.Encode([]rune(s))
	w.u32(uint32(-int32(len(u) + 1)))
	for _, c := range u {
		w.b = binary.LittleEndian.AppendUint16(w.b, c)
	}
	w.b = append(w.b, 0, 0)
}

// WritePak — архив .pak (версия 3: без сжатия и шифрования, его читает любой UE4) с файлами
// files: путь от точки монтирования → содержимое.
func WritePak(mount string, names []string, files map[string][]byte) []byte {
	var out writer
	type rec struct {
		name      string
		off, size int64
		hash      [20]byte
	}
	var recs []rec
	entryHeader := func(w *writer, off, size int64, hash [20]byte) {
		w.u64(uint64(off))
		w.u64(uint64(size))
		w.u64(uint64(size))
		w.u32(0) // без сжатия
		w.b = append(w.b, hash[:]...)
		w.b = append(w.b, 0) // не зашифрован
		w.u32(0)             // размер блока сжатия
	}
	for _, name := range names {
		data := files[name]
		h := sha1sum(data)
		off := int64(len(out.b))
		entryHeader(&out, 0, int64(len(data)), h) // перед данными смещение записывается нулём
		out.b = append(out.b, data...)
		recs = append(recs, rec{name, off, int64(len(data)), h})
	}
	var idx writer
	idx.fstring(mount)
	idx.u32(uint32(len(recs)))
	for _, r := range recs {
		idx.fstring(r.name)
		entryHeader(&idx, r.off, r.size, r.hash)
	}
	indexOff := int64(len(out.b))
	out.b = append(out.b, idx.b...)
	ih := sha1sum(idx.b)
	out.u32(pakMagic) // у версии 3 подвал без флага шифрования и GUID ключа
	out.u32(3)
	out.u64(uint64(indexOff))
	out.u64(uint64(len(idx.b)))
	out.b = append(out.b, ih[:]...)
	return out.b
}

func sha1sum(b []byte) [20]byte { return sha1.Sum(b) }
