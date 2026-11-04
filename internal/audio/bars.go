package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// BARS — звуковой архив Switch (Ring Fit, Splatoon, ARMS…):
//
//	заголовок 0x10: «BARS», размер файла, BOM, версия, число звуков;
//	CRC32 имён (отсортированы), затем пары смещений (AMTA, BWAV) — в том же порядке;
//	блоки AMTA (описание звука, в конце — имя в секции STRG) подряд;
//	звуки BWAV, каждый с границы 0x40.
//
// Звук с пометкой prefetch — только начало музыки: сама она лежит отдельным файлом в Stream/.

// BARS — разобранный архив.
type BARS struct {
	bo      binary.ByteOrder
	head    []byte // всё до первого BWAV: заголовок, таблицы, AMTA — при сборке не меняется
	Entries []BARSEntry
	order   []int // порядок звуков в файле (по смещению BWAV)
	amta    []int // где AMTA каждого звука (внутри head)
	bwavOff []int // где был BWAV каждого звука в исходном архиве (у нескольких звуков может быть один)
}

// BARSEntry — звук архива.
type BARSEntry struct {
	Name     string
	BWAV     []byte // вместе с выравниванием до следующего звука
	Prefetch bool
}

const barsAlign = 0x40

// ParseBARS разбирает архив.
func ParseBARS(b []byte) (*BARS, error) {
	if len(b) < 0x10 || string(b[:4]) != "BARS" {
		return nil, fmt.Errorf("это не BARS")
	}
	bo := order(b[8:10])
	n := int(bo.Uint32(b[0xC:]))
	table := 0x10 + n*4
	if n <= 0 || n > 100000 || table+n*8 > len(b) {
		return nil, fmt.Errorf("BARS: неожиданный заголовок")
	}
	a := &BARS{bo: bo, Entries: make([]BARSEntry, n), amta: make([]int, n)}
	bwav := make([]int, n)
	a.bwavOff = bwav
	for i := range n {
		amta := int(bo.Uint32(b[table+i*8:]))
		a.amta[i] = amta
		bwav[i] = int(bo.Uint32(b[table+i*8+4:]))
		// в старых архивах (Mario Kart 8 Deluxe) звуки — BFWAV, а начала музыки — FSTP; в новых — BWAV
		if bwav[i]+0x10 > len(b) {
			return nil, fmt.Errorf("BARS: звук %d за концом файла", i)
		}
		magic := string(b[bwav[i] : bwav[i]+4])
		if magic != "BWAV" && magic != "FWAV" && magic != "FSTP" {
			return nil, fmt.Errorf("BARS: звук %d — не BWAV и не BFWAV", i)
		}
		a.Entries[i].Name = amtaName(b, amta, bo)
		a.Entries[i].Prefetch = magic == "FSTP" || magic == "BWAV" && bo.Uint16(b[bwav[i]+0xC:]) != 0
		a.order = append(a.order, i)
	}
	sort.SliceStable(a.order, func(x, y int) bool { return bwav[a.order[x]] < bwav[a.order[y]] })
	first := bwav[a.order[0]]
	a.head = append([]byte(nil), b[:first]...)
	for k, i := range a.order {
		end := len(b)
		if k+1 < len(a.order) {
			end = bwav[a.order[k+1]]
		}
		a.Entries[i].BWAV = b[bwav[i]:end]
	}
	return a, nil
}

// amtaName — имя звука из секции STRG его AMTA ("" — не нашлось). В AMTA версии 5 (Mario Wonder) секций нет:
// по смещению 0x24 — расстояние от этого места до имени.
func amtaName(b []byte, off int, bo binary.ByteOrder) string {
	if off+0x28 <= len(b) && string(b[off:off+4]) == "AMTA" && b[off+6]|b[off+7] == 5 {
		at := off + 0x24 + int(bo.Uint32(b[off+0x24:]))
		if at < len(b) {
			name := b[at:]
			if i := bytes.IndexByte(name, 0); i >= 0 {
				return string(name[:i])
			}
		}
		return ""
	}
	if off+0x1C > len(b) || string(b[off:off+4]) != "AMTA" {
		return ""
	}
	strg := off + int(bo.Uint32(b[off+0x18:]))
	if strg+8 > len(b) || string(b[strg:strg+4]) != "STRG" {
		return ""
	}
	n := int(bo.Uint32(b[strg+4:]))
	name := b[strg+8 : min(len(b), strg+8+n)]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return string(name)
}

// Sound — данные звука i; у звука, общего с другим именем, они лежат у того, кто в файле последний.
func (a *BARS) Sound(i int) []byte {
	if len(a.Entries[i].BWAV) > 0 {
		return a.Entries[i].BWAV
	}
	for k := range a.Entries {
		if a.bwavOff[k] == a.bwavOff[i] && len(a.Entries[k].BWAV) > 0 {
			return a.Entries[k].BWAV
		}
	}
	return nil
}

// Find — звук по имени.
func (a *BARS) Find(name string) (int, bool) {
	for i, e := range a.Entries {
		if e.Name == name {
			return i, true
		}
	}
	return -1, false
}

// Bytes собирает архив: таблицы и AMTA как были, звуки — подряд с границы 0x40, смещения пересчитаны.
func (a *BARS) Bytes() []byte {
	out := append([]byte(nil), a.head...)
	table := 0x10 + len(a.Entries)*4
	for _, i := range a.order {
		for len(out)%barsAlign != 0 {
			out = append(out, 0)
		}
		a.bo.PutUint32(out[table+i*8+4:], uint32(len(out)))
		out = append(out, a.Entries[i].BWAV...)
	}
	a.bo.PutUint32(out[4:], uint32(len(out)))
	return out
}

// DecodeBARSEntry — один звук архива.
func DecodeBARSEntry(b []byte, name string) (*PCM, []byte, error) {
	a, err := ParseBARS(b)
	if err != nil {
		return nil, nil, err
	}
	i, ok := a.Find(name)
	if !ok {
		return nil, nil, fmt.Errorf("в архиве нет звука %s", name)
	}
	p, err := Decode(a.Sound(i))
	return p, a.Sound(i), err
}

// ReplaceInBARS вписывает звук вместо одного из звуков архива и собирает архив заново.
func ReplaceInBARS(b []byte, name string, p *PCM) ([]byte, error) {
	a, err := ParseBARS(b)
	if err != nil {
		return nil, err
	}
	i, ok := a.Find(name)
	if !ok {
		return nil, fmt.Errorf("в архиве нет звука %s", name)
	}
	if a.Entries[i].Prefetch {
		return nil, fmt.Errorf("%s — только начало музыки, сама она лежит отдельным файлом в Stream/", name)
	}
	for k := range a.Entries { // звук общий с другим именем: данные лежат у того, кто в файле последний
		if len(a.Entries[i].BWAV) == 0 && a.bwavOff[k] == a.bwavOff[i] && len(a.Entries[k].BWAV) > 0 {
			i = k
		}
	}
	old := a.Entries[i].BWAV
	nb, err := Replace(old, p)
	if err != nil {
		return nil, err
	}
	a.Entries[i].BWAV = nb
	// описание звука (AMTA) знает его длину и частоту — у всех звуков, что делят этот BWAV
	for k := range a.Entries {
		if a.bwavOff[k] == a.bwavOff[i] {
			a.fixAMTA(k, old, nb)
		}
	}
	return a.Bytes(), nil
}

// fixAMTA переписывает в секции DATA описания звука k частоту и число сэмплов — если там стояли
// значения старого BWAV. Иначе игра считает звук прежней длины и читает за концом данных.
func (a *BARS) fixAMTA(k int, old, nb []byte) {
	off := a.amta[k]
	h := a.head
	if off+0x10 > len(h) || string(h[off:off+4]) != "AMTA" {
		return
	}
	d := off + int(a.bo.Uint32(h[off+0xC:]))
	if d+0x24 > len(h) || string(h[d:d+4]) != "DATA" || len(old) < 0x20 || len(nb) < 0x20 {
		return
	}
	bo := order(old[4:6])
	if a.bo.Uint32(h[d+0x18:]) == bo.Uint32(old[0x14:]) {
		a.bo.PutUint32(h[d+0x18:], bo.Uint32(nb[0x14:]))
	}
	if a.bo.Uint32(h[d+0x20:]) == bo.Uint32(old[0x1C:]) {
		a.bo.PutUint32(h[d+0x20:], bo.Uint32(nb[0x1C:]))
	}
}
