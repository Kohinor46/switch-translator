package nx

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Обновление хранит RomFS не целиком, а как патч (BKTR): таблица «кусок новой RomFS →
// кусок базовой игры или кусок обновления». Плюс у кусков обновления разный счётчик шифрования.

const bktrNodeSize = 0x4000

type bucketTree struct {
	entries [][]byte
	end     int64 // конец последнего куска
}

// readBucketTree читает все записи дерева из узлов с записями, минуя узлы-оглавления.
func readBucketTree(r io.ReaderAt, off, size int64, hdr []byte, entrySize int) (*bucketTree, error) {
	if string(hdr[:4]) != "BKTR" {
		return nil, fmt.Errorf("нет сигнатуры BKTR")
	}
	count := int(binary.LittleEndian.Uint32(hdr[8:]))
	if count == 0 {
		return &bucketTree{}, nil // пустая таблица: у почти пустого обновления (NSMBU Deluxe) шифровать нечего
	}
	perSet := (bktrNodeSize - 0x10) / entrySize
	sets := (count + perSet - 1) / perSet
	perNode := (bktrNodeSize - 0x10) / 8
	l2 := 0
	if sets > perNode {
		l2count := (sets + perNode - 1) / perNode
		l2 = (sets - (perNode - (l2count - 1)) + perNode - 1) / perNode
	}
	nodeStorage := int64(1+l2) * bktrNodeSize
	if nodeStorage+int64(sets)*bktrNodeSize > size {
		return nil, fmt.Errorf("таблица BKTR больше отведённого места (записей %d, место %#x, нужно %#x)", count, size, nodeStorage+int64(sets)*bktrNodeSize)
	}
	head := make([]byte, 0x10)
	if _, err := r.ReadAt(head, off); err != nil {
		return nil, err
	}
	t := &bucketTree{end: int64(binary.LittleEndian.Uint64(head[8:]))}
	node := make([]byte, bktrNodeSize)
	for i := range sets {
		if _, err := r.ReadAt(node, off+nodeStorage+int64(i)*bktrNodeSize); err != nil && err != io.EOF {
			return nil, err
		}
		n := int(binary.LittleEndian.Uint32(node[4:]))
		if n > perSet {
			return nil, fmt.Errorf("испорченный узел BKTR")
		}
		for j := range n {
			e := 0x10 + j*entrySize
			t.entries = append(t.entries, append([]byte(nil), node[e:e+entrySize]...))
		}
	}
	if len(t.entries) != count {
		return nil, fmt.Errorf("в BKTR %d записей вместо %d", len(t.entries), count)
	}
	return t, nil
}

// indirectReader собирает новую секцию из кусков базовой игры (0) и обновления (1).
type indirectReader struct {
	virt     []int64
	phys     []int64
	storage  []int32
	end      int64
	storages [2]io.ReaderAt
}

func (ir *indirectReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= ir.end {
		return 0, io.EOF
	}
	done := 0
	for done < len(p) && off < ir.end {
		i := sort.Search(len(ir.virt), func(i int) bool { return ir.virt[i] > off }) - 1
		if i < 0 {
			return done, fmt.Errorf("BKTR: смещение %x вне таблицы", off)
		}
		segEnd := ir.end
		if i+1 < len(ir.virt) {
			segEnd = ir.virt[i+1]
		}
		n := int(min(int64(len(p)-done), segEnd-off))
		st := ir.storages[ir.storage[i]&1]
		got, err := st.ReadAt(p[done:done+n], ir.phys[i]+off-ir.virt[i])
		done += got
		off += int64(got)
		if got < n {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return done, err
		}
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

// ctrExReader расшифровывает секцию обновления: у каждого куска своё «поколение» в счётчике.
type ctrExReader struct {
	r      io.ReaderAt
	base   int64 // смещение секции в NCA
	size   int64
	block  cipher.Block
	ctr    [8]byte
	starts []int64
	gens   []uint32
	plain  []bool
}

func (c *ctrExReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= c.size {
		return 0, io.EOF
	}
	done := 0
	for done < len(p) && off < c.size {
		i := max(sort.Search(len(c.starts), func(i int) bool { return c.starts[i] > off })-1, 0)
		end := c.size
		if i+1 < len(c.starts) {
			end = c.starts[i+1]
		}
		n := int(min(int64(len(p)-done), end-off))
		var err error
		if c.plain[i] {
			_, err = c.r.ReadAt(p[done:done+n], c.base+off)
		} else {
			ctr := c.ctr
			binary.BigEndian.PutUint32(ctr[4:], c.gens[i])
			cr := &ctrReader{r: c.r, base: c.base, size: c.size, block: c.block, ctr: ctr}
			_, err = cr.ReadAt(p[done:done+n], off)
		}
		if err != nil && err != io.EOF {
			return done, err
		}
		done += n
		off += int64(n)
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

// PatchedRomFS накладывает обновление (n — Program NCA обновления) на RomFS базовой игры.
func (n *NCA) PatchedRomFS(base *NCA) (io.ReaderAt, int64, error) {
	r, _, off, size, err := n.patched(base)
	if err != nil {
		return nil, 0, err
	}
	return io.NewSectionReader(r, off, size), size, nil
}

// patched — RomFS с обновлением: читатель всей секции, таблица перемещений (nil — обновление хранит RomFS целиком)
// и где в секции начинаются данные RomFS.
func (n *NCA) patched(base *NCA) (io.ReaderAt, *indirectReader, int64, int64, error) {
	var ps *Section
	for i := range n.Sections {
		if n.Sections[i].FsType == fsRomFS {
			ps = &n.Sections[i]
		}
	}
	if ps == nil {
		return nil, nil, 0, 0, fmt.Errorf("в обновлении нет RomFS")
	}
	if ps.EncType != encBKTR {
		// обновление с полной RomFS (так бывает у модов и редких патчей) — база не нужна
		sec, err := n.SectionReader(*ps)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		off, size, err := ivfcData(ps)
		return sec, nil, off, size, err
	}
	baseSec, err := base.romfsSection()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	baseR, err := base.SectionReader(*baseSec)
	if err != nil {
		return nil, nil, 0, 0, err
	}

	// таблицы лежат в конце секции и зашифрованы обычным счётчиком секции
	var tables io.ReaderAt
	if n.plainBody {
		tables = io.NewSectionReader(n.r, ps.Offset, ps.Size)
	} else {
		if n.keyErr != nil {
			return nil, nil, 0, 0, n.keyErr
		}
		block, _ := aes.NewCipher(n.ctrKey)
		tables = &ctrReader{r: n.r, base: ps.Offset, size: ps.Size, block: block, ctr: ps.Ctr}
	}
	h := ps.Header
	reloc, err := readBucketTree(tables, int64(binary.LittleEndian.Uint64(h[0x100:])), int64(binary.LittleEndian.Uint64(h[0x108:])), h[0x110:0x120], 0x14)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("таблица кусков обновления: %w", err)
	}

	sub, err := readBucketTree(tables, int64(binary.LittleEndian.Uint64(h[0x120:])), int64(binary.LittleEndian.Uint64(h[0x128:])), h[0x130:0x140], 0x10)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("таблица шифрования обновления: %w", err)
	}
	if err := checkPatchInfo(h, sub.end, ps.Size, false); err != nil {
		return nil, nil, 0, 0, err
	}

	var patchR io.ReaderAt
	if n.plainBody {
		// NCZ хранит обновление уже расшифрованным — с правильным счётчиком для каждого куска
		patchR = io.NewSectionReader(n.r, ps.Offset, ps.Size)
	} else {
		block, _ := aes.NewCipher(n.ctrKey)
		cx := &ctrExReader{r: n.r, base: ps.Offset, size: ps.Size, block: block, ctr: ps.Ctr}
		for _, e := range sub.entries {
			cx.starts = append(cx.starts, int64(binary.LittleEndian.Uint64(e)))
			cx.plain = append(cx.plain, e[8] == 1)
			cx.gens = append(cx.gens, binary.LittleEndian.Uint32(e[12:]))
		}
		if len(cx.starts) == 0 { // таблицы нет — вся секция зашифрована обычным счётчиком
			cx.starts, cx.plain, cx.gens = []int64{0}, []bool{false}, []uint32{0}
		}
		patchR = cx
	}

	ir := &indirectReader{end: reloc.end, storages: [2]io.ReaderAt{baseR, patchR}}
	for _, e := range reloc.entries {
		ir.virt = append(ir.virt, int64(binary.LittleEndian.Uint64(e)))
		ir.phys = append(ir.phys, int64(binary.LittleEndian.Uint64(e[8:])))
		ir.storage = append(ir.storage, int32(binary.LittleEndian.Uint32(e[16:])))
	}
	off, size, err := ivfcData(ps)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return ir, ir, off, size, nil
}

// checkPatchInfo — проверки таблиц обновления: таблица перемещений, затем таблица шифрования, обе внутри
// секции. strict — для обновлений, собранных программой: ещё и расшифрованная часть секции (до конца таблицы
// шифрования) должна вмещать таблицу перемещений, иначе ошибка 2002-4070. У официальных обновлений бывает
// иначе (Kirby and the Forgotten Land, Captain Toad) — и консоль их запускает, поэтому при чтении это не проверяется.
func checkPatchInfo(h []byte, ctrEnd, secSize int64, strict bool) error {
	le := binary.LittleEndian
	indOff, indSize := int64(le.Uint64(h[0x100:])), int64(le.Uint64(h[0x108:]))
	ctrOff, ctrSize := int64(le.Uint64(h[0x120:])), int64(le.Uint64(h[0x128:]))
	switch {
	case indSize <= 0 || ctrSize < 0:
		return fmt.Errorf("пустая таблица обновления")
	case indOff+indSize > ctrOff:
		return fmt.Errorf("таблица перемещений налезает на таблицу шифрования")
	case ctrOff+ctrSize > secSize:
		return fmt.Errorf("таблицы обновления выходят за его секцию")
	case strict && indOff+indSize > ctrEnd:
		return fmt.Errorf("таблица перемещений за концом расшифрованной части (%#x > %#x) — консоль не запустит игру (2002-4070)", indOff+indSize, ctrEnd)
	}
	return nil
}

// baseRange — откуда в секции базовой игры взяты байты [v, v+size) секции обновления; false — не из игры целиком.
func (ir *indirectReader) baseRange(v, size int64) (int64, bool) {
	i := sort.Search(len(ir.virt), func(i int) bool { return ir.virt[i] > v }) - 1
	if i < 0 || ir.storage[i]&1 != 0 {
		return 0, false
	}
	end := ir.end
	if i+1 < len(ir.virt) {
		end = ir.virt[i+1]
	}
	if v+size > end {
		return 0, false
	}
	return ir.phys[i] + v - ir.virt[i], true
}

func (n *NCA) romfsSection() (*Section, error) {
	for i := range n.Sections {
		if n.Sections[i].FsType == fsRomFS {
			return &n.Sections[i], nil
		}
	}
	return nil, fmt.Errorf("в NCA нет RomFS")
}

// ivfcData — где внутри секции лежат сами данные RomFS (последний уровень IVFC).
func ivfcData(s *Section) (int64, int64, error) {
	h := s.Header[0x8:]
	if string(h[:4]) != "IVFC" {
		return 0, 0, fmt.Errorf("секция RomFS без IVFC (тип хэша %d)", s.HashType)
	}
	levels := int(binary.LittleEndian.Uint32(h[0xC:])) - 1
	if levels < 1 || levels > 6 {
		levels = 6
	}
	lvl := h[0x10+(levels-1)*0x18:]
	return int64(binary.LittleEndian.Uint64(lvl[0:])), int64(binary.LittleEndian.Uint64(lvl[8:])), nil
}
