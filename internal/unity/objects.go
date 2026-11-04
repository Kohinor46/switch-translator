package unity

import (
	"bytes"
	"encoding/binary"
	"unicode/utf8"
)

// ostream — чтение полей объекта без дерева типов: строки Unity — длина int32, байты, выравнивание 4.
type ostream struct {
	b   []byte
	p   int
	ord binary.ByteOrder
	bad bool
}

func (s *ostream) i32() int32 {
	if s.p+4 > len(s.b) {
		s.bad = true
		return 0
	}
	s.p += 4
	return int32(s.ord.Uint32(s.b[s.p-4:]))
}

func (s *ostream) i64() int64 {
	if s.p+8 > len(s.b) {
		s.bad = true
		return 0
	}
	s.p += 8
	return int64(s.ord.Uint64(s.b[s.p-8:]))
}

func (s *ostream) str() string {
	n := int(s.i32())
	if s.bad || n < 0 || s.p+n > len(s.b) {
		s.bad = true
		return ""
	}
	v := string(s.b[s.p : s.p+n])
	s.p += n
	s.align()
	return v
}

func (s *ostream) align() {
	if m := s.p % 4; m != 0 {
		s.p += 4 - m
	}
}

// PPtr — ссылка на объект: номер файла (0 — этот, иначе Externals[n-1]) и PathID.
type PPtr struct {
	File int32
	Path int64
}

func (s *ostream) pptr() PPtr { return PPtr{s.i32(), s.i64()} }

// ObjectName — m_Name объекта, у которого имя идёт первым полем (TextAsset, MonoScript…).
func (f *File) ObjectName(o Object) string {
	s := &ostream{b: f.Data(o), ord: f.Order()}
	n := s.str()
	if s.bad || !utf8.ValidString(n) {
		return ""
	}
	return n
}

// MonoScript — имя класса скрипта.
type MonoScript struct{ Name, Class, Namespace, Assembly string }

// Script читает MonoScript.
func (f *File) Script(o Object) (MonoScript, bool) {
	s := &ostream{b: f.Data(o), ord: f.Order()}
	var m MonoScript
	m.Name = s.str()
	s.i32()   // m_ExecutionOrder
	s.p += 16 // m_PropertiesHash
	m.Class = s.str()
	m.Namespace = s.str()
	m.Assembly = s.str()
	return m, !s.bad
}

// MonoHeader — общие поля MonoBehaviour: игровой объект, скрипт, имя; Fields — откуда начинаются поля скрипта.
type MonoHeader struct {
	GameObject, Script PPtr
	Name               string
	Fields             int
}

// Mono читает начало MonoBehaviour.
func (f *File) Mono(o Object) (MonoHeader, bool) {
	s := &ostream{b: f.Data(o), ord: f.Order()}
	var h MonoHeader
	h.GameObject = s.pptr()
	s.p++ // m_Enabled
	s.align()
	h.Script = s.pptr()
	h.Name = s.str()
	h.Fields = s.p
	return h, !s.bad
}

// TextScript — содержимое TextAsset (m_Script) и где оно лежит в данных объекта.
func (f *File) TextScript(o Object) (name string, script Span, ok bool) {
	s := &ostream{b: f.Data(o), ord: f.Order()}
	name = s.str()
	_, script = s.span()
	return name, script, !s.bad
}

// ClassAudioClip — AudioClip: звук лежит в отдельном файле (m_Resource: имя, смещение, размер).
const ClassAudioClip = 83

// clipRefs вызывает fn для каждого AudioClip с данными в resource (имя файла данных, конец пути):
// pos — где в raw его смещение (за ним размер, оба uint64).
func clipRefs(raw []byte, resource string, fn func(pos int, ord binary.ByteOrder)) error {
	f, err := ParseSerialized(raw)
	if err != nil {
		return err
	}
	ord := f.Order()
	for _, o := range f.Objects {
		if o.ClassID != ClassAudioClip {
			continue
		}
		start := int(f.DataOffset + o.Offset)
		d := raw[start : start+int(o.Size)]
		// m_Resource.m_Source — строка с именем файла данных; за ней (с выравниванием) смещение и размер
		for p := 0; p+4 <= len(d); p += 4 {
			l := int(ord.Uint32(d[p:]))
			if l < len(resource) || l > 512 || p+4+l > len(d) || !bytes.HasSuffix(d[p+4:p+4+l], []byte(resource)) {
				continue
			}
			q := (p + 4 + l + 3) &^ 3
			if q+16 <= len(d) {
				fn(start+q, ord)
			}
			break
		}
	}
	return nil
}

// AudioClipRefs — смещения и размеры данных всех AudioClip, чьи данные лежат в resource.
func AudioClipRefs(raw []byte, resource string) ([][2]int64, error) {
	var out [][2]int64
	err := clipRefs(raw, resource, func(pos int, ord binary.ByteOrder) {
		out = append(out, [2]int64{int64(ord.Uint64(raw[pos:])), int64(ord.Uint64(raw[pos+8:]))})
	})
	return out, err
}

// PatchAudioClips переписывает у AudioClip, чьи данные лежат в resource, смещение и размер по moved
// (старое смещение → новые смещение и размер). Поля той же длины, поэтому файл правится на месте.
// Возвращает число исправленных звуков.
func PatchAudioClips(raw []byte, resource string, moved map[int64][2]int64) (int, error) {
	n := 0
	err := clipRefs(raw, resource, func(pos int, ord binary.ByteOrder) {
		if m, ok := moved[int64(ord.Uint64(raw[pos:]))]; ok {
			ord.PutUint64(raw[pos:], uint64(m[0]))
			ord.PutUint64(raw[pos+8:], uint64(m[1]))
			n++
		}
	})
	return n, err
}
