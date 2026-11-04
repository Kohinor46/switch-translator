// Package unity читает и пишет файлы Unity: сериализованные файлы ассетов (*.assets, level*, CAB-*)
// и бандлы UnityFS. Из объектов достаются строки для перевода: TextAsset целиком и строки
// внутри MonoBehaviour (таблицы локализации).
package unity

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Классы Unity, которые нам нужны.
const (
	ClassTextAsset     = 49
	ClassMonoBehaviour = 114
	ClassMonoScript    = 115
)

// Object — объект сериализованного файла.
type Object struct {
	PathID  int64
	Offset  int64 // от начала данных (dataOffset)
	Size    uint32
	TypeID  int32
	ClassID int32
	Script  int16 // индекс скрипта MonoBehaviour в Types (или -1)
}

// Type — описание типа объектов.
type Type struct {
	ClassID     int32
	ScriptIndex int16
	ScriptID    [16]byte
	Tree        []TypeNode // пусто, если дерево типов вырезано
}

// TypeNode — поле дерева типов.
type TypeNode struct {
	Type, Name string
	Level      int
	Size       int32
	Flags      uint32 // 0x4000 — выровнять после поля
}

// External — ссылка на другой сериализованный файл.
type External struct{ Path string }

// File — разобранный сериализованный файл.
type File struct {
	Version    uint32
	Unity      string
	Platform   int32
	TypeTree   bool
	Big        bool // порядок байт данных
	DataOffset int64
	Types      []Type
	Objects    []Object
	Externals  []External

	raw []byte
	// где в заголовке лежат смещение и размер каждого объекта — чтобы пересобрать файл
	objFields []objField
	hdr       hdrFields
}

type objField struct{ start, size int } // позиции полей byteStart и byteSize в raw

type hdrFields struct {
	metaSize, fileSize, dataOffset int // позиции полей заголовка
	wide                           bool
}

// IsSerialized — похоже ли на сериализованный файл Unity.
func IsSerialized(b []byte) bool {
	if len(b) < 0x30 {
		return false
	}
	be := binary.BigEndian
	ver := be.Uint32(b[8:])
	if ver < 9 || ver > 30 {
		return false
	}
	if ver >= 22 {
		meta := be.Uint32(b[20:])
		size := be.Uint64(b[24:])
		off := be.Uint64(b[32:])
		return meta > 0 && size >= off && off > 0 && size < 1<<40
	}
	meta, size, off := be.Uint32(b), be.Uint32(b[4:]), be.Uint32(b[12:])
	return meta > 0 && size >= off && off > 0 && meta < size
}

type rd struct {
	b   []byte
	p   int
	ord binary.ByteOrder
	err error
}

func (r *rd) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.p+n > len(r.b) || n < 0 {
		r.err = fmt.Errorf("unity: файл обрывается (%d из %d)", r.p+n, len(r.b))
		return false
	}
	return true
}
func (r *rd) u8() byte {
	if !r.need(1) {
		return 0
	}
	r.p++
	return r.b[r.p-1]
}
func (r *rd) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	r.p += 2
	return r.ord.Uint16(r.b[r.p-2:])
}
func (r *rd) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	r.p += 4
	return r.ord.Uint32(r.b[r.p-4:])
}
func (r *rd) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	r.p += 8
	return r.ord.Uint64(r.b[r.p-8:])
}
func (r *rd) bytes(n int) []byte {
	if !r.need(n) {
		return nil
	}
	r.p += n
	return r.b[r.p-n : r.p]
}
func (r *rd) cstr() string {
	if r.err != nil {
		return ""
	}
	i := bytes.IndexByte(r.b[r.p:], 0)
	if i < 0 {
		r.err = fmt.Errorf("unity: строка без конца")
		return ""
	}
	s := string(r.b[r.p : r.p+i])
	r.p += i + 1
	return s
}
func (r *rd) align(n int) {
	if m := r.p % n; m != 0 {
		r.p += n - m
	}
}

// ParseSerialized разбирает сериализованный файл (данные объектов не копируются).
func ParseSerialized(b []byte) (*File, error) {
	if !IsSerialized(b) {
		return nil, fmt.Errorf("unity: не сериализованный файл")
	}
	f := &File{raw: b}
	r := &rd{b: b, ord: binary.BigEndian}
	r.u32() // размер метаданных
	r.u32() // размер файла
	f.Version = r.u32()
	f.DataOffset = int64(r.u32())
	f.hdr = hdrFields{metaSize: 0, fileSize: 4, dataOffset: 12}
	f.Big = r.u8() != 0
	r.bytes(3)
	if f.Version >= 22 {
		f.hdr = hdrFields{metaSize: 20, fileSize: 24, dataOffset: 32, wide: true}
		r.u32()
		r.u64()
		f.DataOffset = int64(r.u64())
		r.u64()
	}
	if !f.Big {
		r.ord = binary.LittleEndian
	}
	v := f.Version
	f.Unity = r.cstr()
	f.Platform = int32(r.u32())
	if v >= 13 {
		f.TypeTree = r.u8() != 0
	}
	n := int(r.u32())
	if n > 1<<16 {
		return nil, fmt.Errorf("unity: %d типов", n)
	}
	for range n {
		t, err := f.readType(r, false)
		if err != nil {
			return nil, err
		}
		f.Types = append(f.Types, t)
	}
	if v >= 7 && v < 14 {
		r.u32() // bigIDEnabled
	}
	n = int(r.u32())
	if n > 1<<24 {
		return nil, fmt.Errorf("unity: %d объектов", n)
	}
	f.Objects = make([]Object, 0, n)
	f.objFields = make([]objField, 0, n)
	for range n {
		var o Object
		if v >= 14 {
			r.align(4)
			o.PathID = int64(r.u64())
		} else {
			o.PathID = int64(int32(r.u32()))
		}
		var of objField
		of.start = r.p
		if v >= 22 {
			o.Offset = int64(r.u64())
		} else {
			o.Offset = int64(r.u32())
		}
		of.size = r.p
		o.Size = r.u32()
		o.TypeID = int32(r.u32())
		o.Script = -1
		if v < 16 {
			o.ClassID = int32(r.u16())
		} else if int(o.TypeID) >= 0 && int(o.TypeID) < len(f.Types) {
			o.ClassID = f.Types[o.TypeID].ClassID
			o.Script = f.Types[o.TypeID].ScriptIndex
		}
		if v < 11 {
			r.u16()
		}
		if v >= 11 && v < 17 {
			o.Script = int16(r.u16())
		}
		if v == 15 || v == 16 {
			r.u8()
		}
		if r.err != nil {
			return nil, r.err
		}
		f.Objects = append(f.Objects, o)
		f.objFields = append(f.objFields, of)
	}
	if v >= 11 {
		n = int(r.u32())
		for range n {
			r.u32()
			if v >= 14 {
				r.align(4)
				r.u64()
			} else {
				r.u32()
			}
		}
	}
	n = int(r.u32())
	for range n {
		if r.err != nil {
			break
		}
		if v >= 6 {
			r.cstr()
		}
		if v >= 5 {
			r.bytes(16)
			r.u32()
		}
		f.Externals = append(f.Externals, External{Path: r.cstr()})
	}
	if r.err != nil {
		return nil, r.err
	}
	return f, nil
}

func (f *File) readType(r *rd, ref bool) (Type, error) {
	v := f.Version
	var t Type
	t.ClassID = int32(r.u32())
	t.ScriptIndex = -1
	if v >= 16 {
		r.u8() // isStrippedType
	}
	if v >= 17 {
		t.ScriptIndex = int16(r.u16())
	}
	if v >= 13 {
		if ref && t.ScriptIndex >= 0 || v < 16 && t.ClassID < 0 || v >= 16 && t.ClassID == ClassMonoBehaviour {
			copy(t.ScriptID[:], r.bytes(16))
		}
		r.bytes(16) // хэш типа
	}
	if f.TypeTree {
		if v >= 12 || v == 10 {
			nodes := int(r.u32())
			strSize := int(r.u32())
			nodeSize := 24
			if v >= 19 {
				nodeSize = 32
			}
			if nodes < 0 || nodes > 1<<20 || strSize < 0 {
				return t, fmt.Errorf("unity: дерево типов %d узлов", nodes)
			}
			raw := r.bytes(nodes * nodeSize)
			strs := r.bytes(strSize)
			if r.err != nil {
				return t, r.err
			}
			str := func(off uint32) string {
				if off&0x80000000 != 0 {
					return commonString(off & 0x7FFFFFFF)
				}
				if int(off) < len(strs) {
					if i := bytes.IndexByte(strs[off:], 0); i >= 0 {
						return string(strs[off : int(off)+i])
					}
				}
				return ""
			}
			for i := range nodes {
				n := raw[i*nodeSize:]
				t.Tree = append(t.Tree, TypeNode{
					Level: int(n[2]),
					Type:  str(r.ord.Uint32(n[4:])),
					Name:  str(r.ord.Uint32(n[8:])),
					Size:  int32(r.ord.Uint32(n[12:])),
					Flags: r.ord.Uint32(n[20:]),
				})
			}
			if v >= 21 {
				if ref {
					r.cstr()
					r.cstr()
					r.cstr()
				} else {
					deps := int(r.u32())
					r.bytes(deps * 4)
				}
			}
		} else {
			return t, fmt.Errorf("unity: старое дерево типов (версия %d) программа не читает", v)
		}
	}
	return t, r.err
}

// Data — байты объекта.
func (f *File) Data(o Object) []byte {
	start := f.DataOffset + o.Offset
	end := start + int64(o.Size)
	if start < 0 || end > int64(len(f.raw)) {
		return nil
	}
	return f.raw[start:end]
}

// Order — порядок байт данных объектов.
func (f *File) Order() binary.ByteOrder {
	if f.Big {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// Rebuild — файл с заменёнными данными объектов (по индексу в Objects). Объекты кладутся
// в прежнем порядке с выравниванием 8, таблица объектов и размер файла переписываются.
func (f *File) Rebuild(changed map[int][]byte) ([]byte, error) {
	be := binary.BigEndian
	order := f.Order()
	head := append([]byte(nil), f.raw[:f.DataOffset]...)
	// объекты — в порядке смещения
	idx := make([]int, len(f.Objects))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return f.Objects[idx[a]].Offset < f.Objects[idx[b]].Offset })
	var body []byte
	offs := make([]int64, len(f.Objects))
	sizes := make([]uint32, len(f.Objects))
	for _, i := range idx {
		o := f.Objects[i]
		data, ok := changed[i]
		if !ok {
			data = f.Data(o)
			if data == nil {
				return nil, fmt.Errorf("unity: объект %d за концом файла", o.PathID)
			}
		}
		for len(body)%8 != 0 {
			body = append(body, 0)
		}
		offs[i] = int64(len(body))
		sizes[i] = uint32(len(data))
		body = append(body, data...)
	}
	for i, of := range f.objFields {
		if f.Version >= 22 {
			order.PutUint64(head[of.start:], uint64(offs[i]))
		} else {
			if offs[i] > 0xFFFFFFFF {
				return nil, fmt.Errorf("unity: файл больше 4 ГБ")
			}
			order.PutUint32(head[of.start:], uint32(offs[i]))
		}
		order.PutUint32(head[of.size:], sizes[i])
	}
	out := append(head, body...)
	if f.hdr.wide {
		be.PutUint64(out[f.hdr.fileSize:], uint64(len(out)))
	} else {
		be.PutUint32(out[f.hdr.fileSize:], uint32(len(out)))
	}
	return out, nil
}
