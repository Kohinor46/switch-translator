package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// BFSAR — звуковой архив Nintendo: эффекты, голоса персонажей, короткие фразы. Звуки лежат в волновых
// архивах (FWAR) внутри его блока FILE, каждый звук — BFWAV. Имена волновых архивов — в таблице строк STRG;
// звук называется «<имя архива>/<номер>». Архив читается кусками: бывает по сотне мегабайт.

// SARWave — звук BFWAV внутри BFSAR.
type SARWave struct {
	Name      string
	off, size int64
}

// Size — сколько байт занимает звук.
func (w SARWave) Size() int64 { return w.size }

// Offset — где звук начинается в архиве.
func (w SARWave) Offset() int64 { return w.off }

type BFSAR struct {
	Waves []SARWave
}

// IsBFSAR — звуковой архив BFSAR (по первым байтам).
func IsBFSAR(head []byte) bool { return len(head) >= 4 && string(head[:4]) == "FSAR" }

// ParseBFSAR находит все звуки волновых архивов.
func ParseBFSAR(r io.ReaderAt, size int64) (a *BFSAR, err error) {
	defer func() {
		if p := recover(); p != nil {
			a, err = nil, fmt.Errorf("BFSAR: не удалось разобрать: %v", p)
		}
	}()
	head := make([]byte, 0x14)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if !IsBFSAR(head) {
		return nil, fmt.Errorf("это не BFSAR")
	}
	bo := order(head[4:6])
	nblocks := int(bo.Uint16(head[0x10:]))
	refs := make([]byte, 12*nblocks)
	if _, err := r.ReadAt(refs, 0x14); err != nil {
		return nil, err
	}
	blocks := map[uint16][2]int64{}
	for i := range nblocks {
		e := refs[12*i:]
		blocks[bo.Uint16(e)] = [2]int64{int64(bo.Uint32(e[4:])), int64(bo.Uint32(e[8:]))}
	}
	info, okI := blocks[0x2001]
	file, okF := blocks[0x2002]
	if !okI || !okF || info[0]+info[1] > size || file[0]+file[1] > size {
		return nil, fmt.Errorf("BFSAR: нет блоков INFO и FILE")
	}
	read := func(b [2]int64) ([]byte, error) {
		buf := make([]byte, b[1])
		_, err := r.ReadAt(buf, b[0])
		return buf, err
	}
	ib, err := read(info)
	if err != nil {
		return nil, err
	}
	names := map[int]string{} // номер волнового архива → имя
	if strg, ok := blocks[0x2000]; ok && strg[0]+strg[1] <= size {
		if sb, err := read(strg); err == nil {
			names = sarNames(sb, bo)
		}
	}
	rd := reader{ib, bo}
	base := 8
	table := func(typ int) (int, bool) {
		for i := range 8 {
			if rd.u16(base+8*i) == typ {
				return base + rd.s32(base+8*i+4), true
			}
		}
		return 0, false
	}
	warT, ok1 := table(0x2103)
	fileT, ok2 := table(0x2106)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("BFSAR: нет таблиц волновых архивов и файлов")
	}
	nfiles := rd.u32(fileT)
	fileBody := file[0] + 8
	a = &BFSAR{}
	for i := range rd.u32(warT) {
		e := warT + rd.s32(warT+4+8*i+4)
		fid := rd.u32(e)
		if fid >= nfiles {
			continue
		}
		fe := fileT + rd.s32(fileT+4+8*fid+4)
		if rd.u16(fe) != 0x220C { // только файлы внутри архива; внешние лежат рядом отдельными файлами
			continue
		}
		loc := fe + rd.s32(fe+4)
		off := int64(rd.s32(loc + 4))
		if off < 0 {
			continue
		}
		name := names[i]
		if name == "" {
			name = fmt.Sprintf("WAR_%03d", i)
		}
		waves, err := readFWAR(r, fileBody+off, bo)
		if err != nil {
			continue
		}
		for k, w := range waves {
			w.Name = fmt.Sprintf("%s/%03d", name, k)
			a.Waves = append(a.Waves, w)
		}
	}
	if len(a.Waves) == 0 {
		return nil, fmt.Errorf("в BFSAR нет звуков")
	}
	return a, nil
}

// readFWAR — звуки волнового архива по смещению at.
func readFWAR(r io.ReaderAt, at int64, bo binary.ByteOrder) ([]SARWave, error) {
	h := make([]byte, 0x14+12*2)
	if _, err := r.ReadAt(h, at); err != nil || string(h[:4]) != "FWAR" {
		return nil, fmt.Errorf("нет FWAR")
	}
	var info, file [2]int64
	for i := range int(bo.Uint16(h[0x10:])) {
		if i >= 2 {
			break
		}
		e := h[0x14+12*i:]
		b := [2]int64{int64(bo.Uint32(e[4:])), int64(bo.Uint32(e[8:]))}
		switch bo.Uint16(e) {
		case 0x6800:
			info = b
		case 0x6801:
			file = b
		}
	}
	if info[1] < 12 || file[1] == 0 || info[1] > 1<<24 {
		return nil, fmt.Errorf("FWAR без INFO или FILE")
	}
	ib := make([]byte, info[1])
	if _, err := r.ReadAt(ib, at+info[0]); err != nil {
		return nil, err
	}
	n := int(bo.Uint32(ib[8:]))
	var out []SARWave
	for i := range n {
		e := 12 + 12*i
		if e+12 > len(ib) {
			break
		}
		off, sz := int64(int32(bo.Uint32(ib[e+4:]))), int64(bo.Uint32(ib[e+8:]))
		if off < 0 || sz == 0 || off+sz > file[1] {
			continue
		}
		out = append(out, SARWave{off: at + file[0] + 8 + off, size: sz})
	}
	return out, nil
}

// sarNames — имена волновых архивов из STRG: листья дерева поиска связывают номер строки с номером предмета
// (старший байт — тип: 5 — волновой архив).
func sarNames(sb []byte, bo binary.ByteOrder) map[int]string {
	rd := reader{sb, bo}
	out := map[int]string{}
	base := 8
	var strT, tree int
	for i := range 2 {
		switch rd.u16(base + 8*i) {
		case 0x2400:
			strT = base + rd.s32(base+8*i+4)
		case 0x2401:
			tree = base + rd.s32(base+8*i+4)
		}
	}
	if strT == 0 || tree == 0 {
		return out
	}
	str := func(id int) string {
		if id < 0 || id >= rd.u32(strT) {
			return ""
		}
		e := strT + 4 + 12*id
		o, n := strT+rd.s32(e+4), rd.u32(e+8)
		if o < 0 || o+n > len(sb) || n == 0 {
			return ""
		}
		return string(sb[o : o+n-1]) // строка с нулём в конце
	}
	count := rd.u32(tree + 4)
	for i := range count {
		nd := tree + 8 + 0x14*i
		if nd+0x14 > len(sb) {
			break
		}
		if rd.u16(nd)&1 == 0 {
			continue
		}
		item := uint32(rd.u32(nd + 0x10))
		if item>>24 == 5 {
			out[int(item&0xFFFFFF)] = str(rd.u32(nd + 0xC))
		}
	}
	return out
}

// Find — номер звука по имени.
func (a *BFSAR) Find(name string) (int, bool) {
	for i, w := range a.Waves {
		if w.Name == name {
			return i, true
		}
	}
	return 0, false
}

// Wave — байты BFWAV звука i.
func (a *BFSAR) Wave(r io.ReaderAt, i int) ([]byte, error) {
	w := a.Waves[i]
	b := make([]byte, w.size)
	if _, err := r.ReadAt(b, w.off); err != nil && err != io.EOF {
		return nil, err
	}
	return b, nil
}

// FGRPWaves — звуки группы BFGRP (её кладут в архивы SARC отдельно от BFSAR, как в Captain Toad): в группе
// лежат копии волновых архивов, звук называется «W<номер архива в группе>/<номер звука>».
func FGRPWaves(b []byte) []SARWave {
	if len(b) < 0x20 || string(b[:4]) != "FGRP" {
		return nil
	}
	bo := order(b[4:6])
	r := bytes.NewReader(b)
	var out []SARWave
	k := 0
	for off := 0x20; off+4 <= len(b); {
		i := bytes.Index(b[off:], []byte("FWAR"))
		if i < 0 {
			break
		}
		at := off + i
		waves, err := readFWAR(r, int64(at), bo)
		if err != nil || at+0x10 > len(b) {
			off = at + 4
			continue
		}
		for n, w := range waves {
			w.Name = fmt.Sprintf("W%02d/%03d", k, n)
			out = append(out, w)
		}
		k++
		off = at + max(4, int(bo.Uint32(b[at+0xC:]))) // дальше конца этого FWAR
	}
	return out
}

// FGRPWave — байты BFWAV звука name из группы BFGRP.
func FGRPWave(b []byte, name string) ([]byte, error) {
	for _, w := range FGRPWaves(b) {
		if w.Name == name {
			return b[w.off : w.off+w.size], nil
		}
	}
	return nil, fmt.Errorf("в группе нет звука %s", name)
}
