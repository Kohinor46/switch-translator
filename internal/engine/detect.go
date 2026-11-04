// Package engine определяет движок игры по файлам RomFS и находит текст и звук.
package engine

import (
	"bytes"
	"encoding/binary"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"switch-translator/internal/codec"
	"switch-translator/internal/unity"
)

type Category string

const (
	Text       Category = "text"
	Audio      Category = "audio"
	Video      Category = "video"
	Archive    Category = "archive"
	Compressed Category = "compressed"
	Graphics   Category = "graphics"
	Font       Category = "font"
	Service    Category = "service" // шейдеры, эффекты, настройки — текста для перевода там нет
	Unknown    Category = "unknown"
)

var categoryOrder = []Category{Text, Audio, Font, Video, Archive, Graphics, Service, Compressed, Unknown}

func (c Category) Title() string {
	switch c {
	case Text:
		return "текст"
	case Audio:
		return "звук"
	case Video:
		return "видео"
	case Archive:
		return "архив"
	case Compressed:
		return "сжатое"
	case Graphics:
		return "графика"
	case Font:
		return "шрифт"
	case Service:
		return "служебное"
	}
	return "неизвестно"
}

type Entry struct {
	Path string
	Size int64
	R    io.ReaderAt
}

type FileInfo struct {
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	Format   string   `json:"format"`
	Category Category `json:"category"`
	Wrapper  string   `json:"wrapper,omitempty"`
	// Inner — что лежит внутри архива SARC: расширение → сколько файлов.
	Inner map[string]int `json:"inner,omitempty"`
}

type Engine struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Tools  string `json:"tools"`
}

type Stat struct {
	Category Category
	Format   string
	Count    int
	Size     int64
}

type Report struct {
	Files   []FileInfo `json:"files"`
	Engines []Engine   `json:"engines"`
}

type magic struct {
	sig    string
	format string
	cat    Category
}

var magics = []magic{
	{"MsgStdBn", "MSBT (тексты Nintendo LMS)", Text},
	{"MsgPrjBn", "MSBP (проект LMS)", Text},
	{"MsgFlwBn", "MSBF (ветки диалогов LMS)", Text},
	{"SARC", "SARC (архив Nintendo)", Archive},
	{"RARC", "RARC (архив GameCube/Wii)", Archive},
	{"CRAR", "RARC (архив GameCube/Wii, порт)", Archive},
	{"FSTM", "BFSTM (стрим-аудио Nintendo)", Audio},
	{"FWAV", "BFWAV (аудио Nintendo)", Audio},
	{"BWAV", "BWAV (аудио Nintendo)", Audio},
	{"BARS", "BARS (аудиоархив Nintendo)", Audio},
	{"BKHD", "Wwise SoundBank (.bnk)", Audio},
	{"AKPK", "Wwise File Package (.pck)", Audio},
	{"CPK ", "CRI CPK (архив CRIWARE)", Archive},
	{"@UTF", "CRI ACB / @UTF-таблица", Audio},
	{"AFS2", "CRI AWB (банк аудио)", Audio},
	{"HCA\x00", "CRI HCA (аудио)", Audio},
	{"\xc8\xc3\xc1\x00", "CRI HCA, зашифрованный", Audio},
	{"CRID", "CRI USM (видео)", Video},
	{"KB2", "Bink 2 (видео)", Video},
	{"BIK", "Bink (видео)", Video},
	{"\x1a\x45\xdf\xa3", "WebM (видео)", Video},
	{"FSB5", "FMOD FSB5 (сэмплы)", Audio},
	{"UnityFS", "Unity AssetBundle", Archive},
	{"GDPC", "Godot PCK", Archive},
	{"RPA-", "Ren'Py RPA", Archive},
	{"OggS", "Ogg Vorbis/Opus", Audio},
	{"fLaC", "FLAC", Audio},
	{"ID3", "MP3", Audio},
	{"PK\x03\x04", "ZIP", Archive},
	{"BNTX", "BNTX (текстуры)", Graphics},
	{"FRES", "BFRES (модели)", Graphics},
	{"FFNT", "BFFNT (шрифт Nintendo)", Font},
	{"FSAR", "BFSAR (звуковой архив: эффекты и короткие звуки)", Audio},
}

var textExts = map[string]bool{
	".json": true, ".csv": true, ".tsv": true, ".txt": true, ".xml": true, ".ini": true,
	".yaml": true, ".yml": true, ".lua": true, ".po": true, ".rpy": true, ".ks": true,
	".tbl": true, ".loc": true, ".lang": true, ".strings": true,
}

// knownExts — форматы без сигнатуры (или с неудобной), которые часто встречаются в играх Switch.
var knownExts = map[string]struct {
	format string
	cat    Category
}{
	".bffnt":  {"BFFNT (шрифт Nintendo)", Font},
	".bfttf":  {"BFTTF (шрифт Nintendo)", Font},
	".bfotf":  {"BFOTF (шрифт Nintendo)", Font},
	".ttf":    {"TTF (шрифт)", Font},
	".otf":    {"OTF (шрифт)", Font},
	".bfsar":  {"BFSAR (звуковой архив: эффекты и короткие звуки)", Audio},
	".opus":   {"Nintendo Opus (.opus)", Audio}, // заголовок 0x80000001 — не Ogg; читает vgmstream
	".ast":    {"AST (стрим-аудио Wii)", Audio}, // Super Mario Galaxy; читает vgmstream
	".lopus":  {"Nintendo Opus (.lopus)", Audio},
	".ptcl":   {"PTCL (эффекты частиц)", Service},
	".sharc":  {"SHARC (шейдеры)", Service},
	".sharcb": {"SHARCB (шейдеры)", Service},
	".gsh":    {"GSH (шейдеры)", Service},
	".fsh":    {"FSH (исходник шейдера)", Service},
	".vsh":    {"VSH (исходник шейдера)", Service},
	".glsl":   {"GLSL (исходник шейдера)", Service},
	".fsdb":   {"FSDB (база шейдеров)", Service},
	".aglenv": {"AGLENV (настройки освещения)", Service},
	".bnsh":   {"BNSH (шейдеры)", Service},
	".bat":    {"BAT (служебный скрипт)", Service},
	".byml":   {"BYML (данные уровней и объектов)", Service},
	".byaml":  {"BYAML (данные уровней и объектов)", Service},
	".bin":    {"BIN (двоичные данные)", Unknown},
	".gtx":    {"GTX (текстуры)", Graphics},
	".tga":    {"TGA (картинка)", Graphics},
	".png":    {"PNG (картинка)", Graphics},
	".jpg":    {"JPEG (картинка)", Graphics},
	".dds":    {"DDS (текстуры)", Graphics},
	".bflim":  {"BFLIM (картинка интерфейса)", Graphics},
	".bflyt":  {"BFLYT (макет интерфейса)", Graphics},
	".bflan":  {"BFLAN (анимация интерфейса)", Graphics},
	".mp4":    {"MP4 (видео)", Video},
	".webm":   {"WebM (видео)", Video},
	".mov":    {"MOV (видео)", Video},
	".bk2":    {"Bink 2 (видео)", Video},
	".bik":    {"Bink (видео)", Video},
	".usm":    {"CRI USM (видео)", Video},
	".thp":    {"THP (видео Wii)", Video},
}

var unityNames = map[string]bool{"globalgamemanagers": true, "data.unity3d": true, "maindata": true, "resources.assets": true}

const peekSize = 64

func Classify(e Entry) FileInfo {
	name := path.Base(e.Path)
	ext := strings.ToLower(path.Ext(name))
	fi := FileInfo{Path: e.Path, Size: e.Size}

	buf := make([]byte, min(e.Size, 64*1024))
	n, _ := e.R.ReadAt(buf, 0)
	buf = buf[:n]
	head := buf[:min(len(buf), peekSize)]

	fi.Wrapper = codec.Wrapper(head)
	innerExt := ext
	switch fi.Wrapper {
	case "Yaz0":
		head, _ = codec.Yaz0(buf, peekSize)
	case "zstd":
		var err error
		head, err = codec.ZstdPeek(io.NewSectionReader(e.R, 0, e.Size), peekSize)
		if err != nil {
			fi.Format, fi.Category = "zstd-сжатый файл", Compressed
			return fi
		}
	case "lz4", "lz4s":
		head, _ = codec.LZ4(buf, peekSize)
	}
	if fi.Wrapper != "" && (ext == ".zs" || ext == ".szs" || ext == ".cmpbin") {
		innerExt = strings.ToLower(path.Ext(strings.TrimSuffix(name, path.Ext(name))))
	}

	fi.Format, fi.Category = matchMagic(head, innerExt)
	if fi.Category == "" {
		switch {
		case ext == ".pak" && unrealFooter(e):
			fi.Format, fi.Category = "Unreal PAK", Archive
		case ext == ".utoc" || ext == ".ucas":
			fi.Format, fi.Category = "Unreal IoStore (utoc/ucas)", Archive
		case ext == ".dat" && ttDAT(e):
			fi.Format, fi.Category = "TT Games DAT", Archive
		case ext == ".locres":
			fi.Format, fi.Category = "Unreal .locres (локализация)", Text
		case fi.Wrapper == "" && unity.IsSerialized(buf):
			fi.Format, fi.Category = "Unity serialized (ассеты)", Archive
		case unityNames[strings.ToLower(name)] || ext == ".assets" || ext == ".ress" || ext == ".resource" || strings.HasPrefix(name, "sharedassets"):
			fi.Format, fi.Category = "Unity resource (данные ассетов)", Archive
		case knownExts[innerExt].format != "":
			fi.Format, fi.Category = knownExts[innerExt].format, knownExts[innerExt].cat
		case fi.Wrapper == "" && textExts[ext]:
			fi.Format, fi.Category = "текст "+orNone(ext), Text
		case fi.Wrapper == "" && looksLikeText(buf):
			// текст, но не в формате реплик: настройки, скрипты сборки, исходники
			fi.Format, fi.Category = "служебный текстовый файл "+orNone(ext), Service
		default:
			fi.Format, fi.Category = "неизвестно "+orNone(innerExt), Unknown
		}
	}
	if strings.HasPrefix(fi.Format, "SARC") {
		fi.Inner = sarcInner(e, buf, fi.Wrapper)
		if onlyFonts(fi.Inner) { // архив только со шрифтами — это шрифты
			fi.Category = Font
		}
	}
	if fi.Wrapper != "" {
		fi.Format += " [" + fi.Wrapper + "]"
	}
	return fi
}

// onlyFonts — в архиве одни шрифты: растровые BFFNT или векторные BFTTF/BFOTF.
func onlyFonts(inner map[string]int) bool {
	for ext := range inner {
		if knownExts[ext].cat != Font {
			return false
		}
	}
	return len(inner) > 0
}

// sarcInner читает только начало архива — там таблица имён — и считает файлы по расширениям.
func sarcInner(e Entry, buf []byte, wrapper string) map[string]int {
	const want = 256 * 1024
	data := buf
	switch wrapper {
	case "Yaz0":
		data, _ = codec.Yaz0(buf, want)
	case "zstd":
		data, _ = codec.ZstdPeek(io.NewSectionReader(e.R, 0, e.Size), want)
	}
	names := sarcNames(data)
	if len(names) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, n := range names {
		ext := strings.ToLower(path.Ext(n))
		if ext == ".zs" || ext == ".szs" {
			ext = strings.ToLower(path.Ext(strings.TrimSuffix(n, path.Ext(n)))) + ext
		}
		out[orNone(ext)]++
	}
	return out
}

// sarcNames — имена файлов из SFAT/SFNT; данные самих файлов не нужны.
func sarcNames(d []byte) []string {
	if len(d) < 0x20 || string(d[:4]) != "SARC" {
		return nil
	}
	le := d[6] == 0xFF
	u16 := func(b []byte) int {
		if le {
			return int(b[0]) | int(b[1])<<8
		}
		return int(b[1]) | int(b[0])<<8
	}
	hdr := u16(d[4:])
	if hdr+0xC > len(d) || string(d[hdr:hdr+4]) != "SFAT" {
		return nil
	}
	count := u16(d[hdr+6:])
	sfnt := hdr + 0xC + count*0x10
	if sfnt+8 > len(d) || string(d[sfnt:sfnt+4]) != "SFNT" {
		return nil
	}
	var names []string
	for p := sfnt + 8; p < len(d) && len(names) < count; {
		z := bytes.IndexByte(d[p:], 0)
		if z < 0 {
			break // таблица имён не влезла в прочитанное начало — берём, что успели
		}
		if z > 0 {
			names = append(names, string(d[p:p+z]))
		}
		p = (p + z + 4) &^ 3
	}
	return names
}

// ArchiveContents — сводка по содержимому всех архивов SARC.
func (r *Report) ArchiveContents() map[string]int {
	out := map[string]int{}
	for _, f := range r.Files {
		for ext, n := range f.Inner {
			out[ext] += n
		}
	}
	return out
}

// Describe — человеческое название расширения для сводки по архивам.
func Describe(ext string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(ext, ".zs"), ".szs")
	if k, ok := knownExts[base]; ok {
		return k.format
	}
	if d, ok := innerNames[base]; ok {
		return d
	}
	return ext
}

var innerNames = map[string]string{
	".bfres":  "модели и текстуры (.bfres)",
	".bntx":   "текстуры (.bntx)",
	".msbt":   "тексты (.msbt)",
	".msbp":   "настройки текстов (.msbp)",
	".bfstm":  "музыка и озвучка (.bfstm)",
	".bfwav":  "звуки (.bfwav)",
	".bwav":   "звуки (.bwav)",
	".bfevfl": "сценарии событий (.bfevfl)",
	".bfsha":  "шейдеры (.bfsha)",
	".bars":   "звуковые архивы (.bars)",
	".sarc":   "вложенные архивы (.sarc)",
	".aamp":   "параметры объектов (.aamp)",
	".bxml":   "XML-данные (.bxml)",
	".xml":    "XML-данные (.xml)",
	".kcl":    "коллизии уровня (.kcl)",
	".bfsar":  "звуковой архив (.bfsar)",
}

func orNone(ext string) string {
	if ext == "" {
		return "(без расширения)"
	}
	return ext
}

func matchMagic(head []byte, ext string) (string, Category) {
	for _, m := range magics {
		if bytes.HasPrefix(head, []byte(m.sig)) {
			return m.format, m.cat
		}
	}
	if len(head) >= 8 && string(head[4:8]) == "ftyp" {
		return "MP4 (видео)", Video
	}
	if bytes.HasPrefix(head, []byte("RIFF")) && len(head) >= 12 {
		switch string(head[8:12]) {
		case "WAVE":
			if ext == ".wem" {
				return "Wwise WEM", Audio
			}
			return "WAV", Audio
		case "FEV ":
			return "FMOD Bank (.bank)", Audio
		}
	}
	if bytes.HasPrefix(head, []byte("FORM")) && len(head) >= 12 && string(head[8:12]) == "GEN8" {
		return "GameMaker data.win", Archive
	}
	return "", ""
}

func looksLikeText(buf []byte) bool {
	if bytes.HasPrefix(buf, []byte{0xFF, 0xFE}) || bytes.HasPrefix(buf, []byte{0xFE, 0xFF}) {
		return true
	}
	sample := buf[:min(len(buf), 4096)]
	if len(sample) == 0 {
		return false
	}
	// обрезанный многобайтный символ в конце сэмпла — не повод отказывать
	for i := 0; i < 3 && !utf8.Valid(sample) && len(sample) > 0; i++ {
		sample = sample[:len(sample)-1]
	}
	if !utf8.Valid(sample) {
		return false
	}
	total, printable := 0, 0
	for _, r := range string(sample) {
		total++
		if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r != 0x7F) {
			printable++
		}
	}
	return float64(printable)/float64(total) > 0.95
}

// Магия FPakInfo 0x5A6F12E1 лежит в последних ~200 байтах, смещение зависит от версии.
// ttDAT — архив Traveller's Tales (LEGO): в начале смещение оглавления, а там «.CC40TAD».
func ttDAT(e Entry) bool {
	head := make([]byte, 8)
	if _, err := e.R.ReadAt(head, 0); err != nil {
		return false
	}
	off := int64(binary.LittleEndian.Uint32(head))
	if off&0x80000000 != 0 {
		off = int64(binary.LittleEndian.Uint32(head)^0xFFFFFFFF)<<8 + 0x100
	}
	if off <= 0 || off+12 > e.Size {
		return false
	}
	magic := make([]byte, 8)
	e.R.ReadAt(magic, off+4)
	return string(magic) == ".CC40TAD"
}

func unrealFooter(e Entry) bool {
	n := min(e.Size, 512)
	buf := make([]byte, n)
	e.R.ReadAt(buf, e.Size-n)
	return bytes.Contains(buf, []byte{0xE1, 0x12, 0x6F, 0x5A})
}

func Scan(entries []Entry, progress func(done, total int)) *Report {
	r := &Report{}
	for i, e := range entries {
		r.Files = append(r.Files, Classify(e))
		if progress != nil && (i%64 == 0 || i == len(entries)-1) {
			progress(i+1, len(entries))
		}
	}
	r.Engines = detectEngines(r.Files)
	return r
}

func (r *Report) Stats() []Stat {
	m := map[[2]string]*Stat{}
	for _, f := range r.Files {
		k := [2]string{string(f.Category), f.Format}
		s, ok := m[k]
		if !ok {
			s = &Stat{Category: f.Category, Format: f.Format}
			m[k] = s
		}
		s.Count++
		s.Size += f.Size
	}
	out := make([]Stat, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	rank := func(c Category) int {
		for i, x := range categoryOrder {
			if x == c {
				return i
			}
		}
		return len(categoryOrder)
	}
	sort.Slice(out, func(i, j int) bool {
		if rank(out[i].Category) != rank(out[j].Category) {
			return rank(out[i].Category) < rank(out[j].Category)
		}
		return out[i].Size > out[j].Size
	})
	return out
}

// Of возвращает файлы категории, крупнейшие первыми.
func (r *Report) Of(c Category) []FileInfo {
	var out []FileInfo
	for _, f := range r.Files {
		if f.Category == c {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

var engineTools = map[string]string{
	"Nintendo LMS / SARC": "встроено: MSBT внутри SARC/Yaz0/zstd извлекается автоматически",
	"Unity":               "AssetRipper, UABEA, AssetStudio; тексты в TextAsset/MonoBehaviour (часто I2 Localization)",
	"Unreal Engine":       "repak / FModel (может понадобиться AES-ключ); тексты в Localization/*/*.locres",
	"CRIWARE":             "YACpkTool / CriFsV2Lib для CPK; ACB/AWB/HCA → vgmstream",
	"Wwise":               "vgmstream (wem/bnk), bnkextr",
	"FMOD":                "vgmstream, python-fsb5",
	"Godot":               "GDRE Tools",
	"Ren'Py":              "unrpa, unrpyc",
	"GameMaker":           "UndertaleModTool",
	"RPG Maker MV/MZ":     "тексты прямо в data/*.json",
}

func detectEngines(files []FileInfo) []Engine {
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.Format)
		all.WriteByte('\n')
	}
	fmts := all.String()
	has := func(s string) bool { return strings.Contains(fmts, s) }
	anyPath := func(suffixes ...string) bool {
		for _, f := range files {
			p := strings.ToLower(f.Path)
			for _, s := range suffixes {
				if strings.HasSuffix(p, s) {
					return true
				}
			}
		}
		return false
	}

	var out []Engine
	add := func(name, reason string) { out = append(out, Engine{name, reason, engineTools[name]}) }
	if has("Unity") {
		add("Unity", "найдены globalgamemanagers / *.assets / AssetBundle")
	}
	if has("Unreal") {
		add("Unreal Engine", "найдены .pak / .utoc / .locres")
	}
	if has("MSBT") || has("SARC") || has("BFSTM") || has("BWAV") {
		add("Nintendo LMS / SARC", "найдены MSBT / SARC / BFSTM (стиль first-party Nintendo)")
	}
	if has("CRI ") {
		add("CRIWARE", "найдены CPK / ACB / AWB / HCA / USM")
	}
	if has("Wwise") {
		add("Wwise", "найдены .bnk / .pck / .wem")
	}
	if has("FMOD") {
		add("FMOD", "найдены FSB5 / .bank")
	}
	if has("Godot") {
		add("Godot", "найден .pck с сигнатурой GDPC")
	}
	if has("Ren'Py") || anyPath(".rpy", ".rpyc") {
		add("Ren'Py", "найдены .rpa / .rpy / .rpyc")
	}
	if has("GameMaker") {
		add("GameMaker", "найден data.win")
	}
	if anyPath("js/rpg_core.js", "js/rmmz_core.js") {
		add("RPG Maker MV/MZ", "найдены rpg_core.js / rmmz_core.js")
	}
	return out
}
