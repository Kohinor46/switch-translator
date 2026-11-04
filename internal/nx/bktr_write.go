package nx

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Обновление-разница (BKTR), как у официальных обновлений: секция RomFS обновления хранит только
// то, чего нет в базовой игре (новые и изменённые файлы, таблицы RomFS, хэши), а таблица перемещений
// говорит консоли, какие куски брать из игры. Так обновление с переводом весит мегабайты, а не всю игру.
//
// Шифрование: одна запись AesCtrEx с поколением 0 — это ровно обычный счётчик секции, которым WriteNCA
// шифрует всю секцию. Таблицы лежат в конце секции и шифруются тем же счётчиком.

type indEntry struct {
	virt, phys int64
	storage    int32 // 0 — базовая игра, 1 — это обновление
}

// PatchRomFSSection собирает секцию RomFS обновления. Файлы с InBase берутся из игры.
// tmp — пустой временный файл для байтов обновления. Возвращает и сколько байт попадёт в обновление.
func PatchRomFSSection(files []BuildFile, tmp io.ReadWriteSeeker) (NCASection, int64, error) {
	img, err := LayoutRomFS(files)
	if err != nil {
		return NCASection{}, 0, err
	}
	h := newIVFCHasher(io.Discard)
	cw := &countWriter{w: tmp}
	segs, err := img.writeSplit(h, cw)
	if err != nil {
		return NCASection{}, 0, err
	}
	tree := h.finish()
	hs := tree.hashSize()

	// таблица перемещений: хэши и куски обновления подряд, куски игры — по своим смещениям
	entries := []indEntry{{0, 0, 1}}
	patchPos := hs
	add := func(e indEntry, size int64) {
		last := entries[len(entries)-1]
		// продолжение предыдущего куска — новая запись не нужна
		if last.storage == e.storage && e.phys-last.phys == e.virt-last.virt {
			return
		}
		entries = append(entries, e)
	}
	for _, s := range segs {
		if s.base {
			add(indEntry{hs + s.off, s.phys, 0}, s.size)
			continue
		}
		add(indEntry{hs + s.off, patchPos, 1}, s.size)
		patchPos += s.size
	}
	if patchPos != hs+cw.n {
		return NCASection{}, 0, fmt.Errorf("внутренняя ошибка раскладки обновления: %d != %d", patchPos, hs+cw.n)
	}
	virtEnd := hs + tree.dataSize

	tablesOff := align(patchPos, ivfcBlock)
	ind := make([][]byte, len(entries))
	for i, e := range entries {
		b := make([]byte, 0x14)
		binary.LittleEndian.PutUint64(b, uint64(e.virt))
		binary.LittleEndian.PutUint64(b[8:], uint64(e.phys))
		binary.LittleEndian.PutUint32(b[16:], uint32(e.storage))
		ind[i] = b
	}
	indTable, err := writeBucketTree(ind, 0x14, virtEnd)
	if err != nil {
		return NCASection{}, 0, err
	}
	ctrOff := tablesOff + int64(len(indTable))
	// таблица шифрования покрывает и таблицу перемещений: консоль читает её через расшифрованную секцию,
	// размер которой — конец этой таблицы. Короче — ошибка 2002-4070 при запуске (как у Nintendo: до ctrOff)
	ctrTable, err := writeBucketTree([][]byte{make([]byte, 0x10)}, 0x10, ctrOff) // смещение 0, зашифровано, поколение 0
	if err != nil {
		return NCASection{}, 0, err
	}
	size := ctrOff + int64(len(ctrTable))

	fh := tree.fsHeader()
	fh[4] = encBKTR
	le := binary.LittleEndian
	patchInfo := func(at int, off int64, table []byte, count int) {
		le.PutUint64(fh[at:], uint64(off))
		le.PutUint64(fh[at+8:], uint64(len(table)))
		copy(fh[at+0x10:], "BKTR")
		le.PutUint32(fh[at+0x14:], 1)
		le.PutUint32(fh[at+0x18:], uint32(count))
	}
	patchInfo(0x100, tablesOff, indTable, len(entries))
	patchInfo(0x120, ctrOff, ctrTable, 1)
	if err := checkPatchInfo(fh, ctrOff, size, true); err != nil {
		return NCASection{}, 0, fmt.Errorf("собранное обновление: %w", err)
	}

	data := cw.n
	return NCASection{
		Header: fh,
		Size:   size,
		BKTR:   true,
		Data: func(w io.Writer) error {
			c := &countWriter{w: w}
			for _, l := range tree.levels {
				if _, err := c.Write(l); err != nil {
					return err
				}
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.Copy(c, io.LimitReader(tmp, data)); err != nil {
				return err
			}
			if err := c.pad(tablesOff); err != nil {
				return err
			}
			if _, err := c.Write(indTable); err != nil {
				return err
			}
			_, err := c.Write(ctrTable)
			return err
		},
	}, patchPos, nil
}

// writeBucketTree — таблица BKTR: узел-оглавление (начало каждого набора) и наборы записей по 0x4000.
func writeBucketTree(entries [][]byte, entrySize int, end int64) ([]byte, error) {
	perSet := (bktrNodeSize - 0x10) / entrySize
	sets := max((len(entries)+perSet-1)/perSet, 1)
	if sets > (bktrNodeSize-0x10)/8 {
		return nil, fmt.Errorf("таблица BKTR слишком большая: %d записей", len(entries))
	}
	le := binary.LittleEndian
	out := make([]byte, bktrNodeSize*(1+sets))
	le.PutUint32(out[4:], uint32(sets))
	le.PutUint64(out[8:], uint64(end))
	for s := range sets {
		part := entries[s*perSet : min((s+1)*perSet, len(entries))]
		start := int64(0)
		if len(part) > 0 {
			start = int64(le.Uint64(part[0]))
		}
		le.PutUint64(out[0x10+s*8:], uint64(start))
		node := out[bktrNodeSize*(1+s):]
		le.PutUint32(node[0:], uint32(s))
		le.PutUint32(node[4:], uint32(len(part)))
		next := end
		if (s+1)*perSet < len(entries) {
			next = int64(le.Uint64(entries[(s+1)*perSet]))
		}
		le.PutUint64(node[8:], uint64(next))
		for i, e := range part {
			copy(node[0x10+i*entrySize:], e)
		}
	}
	return out, nil
}
