// Package whisper — распознавание речи whisper.cpp, собранное прямо в программу.
// Исходники whisper.cpp и ggml лежат в csrc (лицензия MIT, csrc/LICENSE),
// файлы w_*.c/w_*.cpp подключают их, чтобы cgo собрал всё обычным go build.
package whisper

/*
#cgo CPPFLAGS: -DNDEBUG -DGGML_USE_CPU -DGGML_USE_LLAMAFILE -DGGML_SCHED_MAX_COPIES=4
#cgo CPPFLAGS: -I${SRCDIR}/csrc/include -I${SRCDIR}/csrc/ggml/include -I${SRCDIR}/csrc/ggml/src -I${SRCDIR}/csrc/ggml/src/ggml-cpu
#cgo CPPFLAGS: -Wno-unused-function -Wno-unused-variable -Wno-deprecated-declarations
#cgo CFLAGS: -O3 -std=c11
#cgo CXXFLAGS: -O3 -std=c++17
#cgo darwin CPPFLAGS: -D_DARWIN_C_SOURCE -DGGML_USE_ACCELERATE -DACCELERATE_NEW_LAPACK -DACCELERATE_LAPACK_ILP64
#cgo darwin LDFLAGS: -framework Accelerate -framework Foundation
#cgo linux CPPFLAGS: -D_GNU_SOURCE -D_XOPEN_SOURCE=600
#cgo linux LDFLAGS: -lm -lpthread
#cgo windows CPPFLAGS: -D_CRT_SECURE_NO_WARNINGS
#cgo amd64 CFLAGS: -mavx2 -mfma -mf16c
#cgo amd64 CXXFLAGS: -mavx2 -mfma -mf16c

#include <stdlib.h>
#include "swtr.h"
*/
import "C"

import (
	"errors"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/cgo"
	"strings"
	"sync"
	"unicode"
	"unsafe"
)

// SampleRate — whisper принимает моно 16 кГц.
const SampleRate = 16000

func init() { C.swtr_init() }

// Model — загруженная модель. Распознавание на одной модели идёт по очереди.
type Model struct {
	mu  sync.Mutex
	ctx *C.struct_whisper_context
}

// Load читает модель ggml-*.bin.
func Load(path string) (*Model, error) { return load(path, false) }

// LoadAligned — модель с выравниванием слов по звуку (DTW): по нему видно, когда фраза на самом деле
// началась и кончилась (нужно для субтитров). Для звука выравнивание не нужно — без него меньше работы.
func LoadAligned(path string) (*Model, error) { return load(path, true) }

func load(path string, aligned bool) (*Model, error) {
	cp := C.CString(path)
	defer C.free(unsafe.Pointer(cp))
	params := C.whisper_context_default_params()
	// flash attention выключен: с коротким окном (AudioCtx) он ломает распознавание у large-v3-turbo,
	// а без него и с коротким окном выходит быстрее всего; DTW с ним тоже не работает
	params.flash_attn = false
	if aligned {
		params.dtw_token_timestamps = true
		params.dtw_aheads_preset = alignmentHeads(filepath.Base(path))
		params.dtw_n_top = 2
	}
	ctx := C.whisper_init_from_file_with_params(cp, params)
	if ctx == nil {
		return nil, errors.New("не удалось загрузить модель Whisper: " + path)
	}
	return &Model{ctx: ctx}, nil
}

// alignmentHeads — набор «голов внимания» для выравнивания, свой у каждой модели.
func alignmentHeads(name string) C.enum_whisper_alignment_heads_preset {
	name = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(name), "ggml-"), ".bin")
	name, _, _ = strings.Cut(name, "-q") // квантованные: ggml-base-q5_1.bin
	switch name {
	case "tiny":
		return C.WHISPER_AHEADS_TINY
	case "tiny.en":
		return C.WHISPER_AHEADS_TINY_EN
	case "base":
		return C.WHISPER_AHEADS_BASE
	case "base.en":
		return C.WHISPER_AHEADS_BASE_EN
	case "small":
		return C.WHISPER_AHEADS_SMALL
	case "small.en":
		return C.WHISPER_AHEADS_SMALL_EN
	case "medium":
		return C.WHISPER_AHEADS_MEDIUM
	case "medium.en":
		return C.WHISPER_AHEADS_MEDIUM_EN
	case "large-v1":
		return C.WHISPER_AHEADS_LARGE_V1
	case "large-v2":
		return C.WHISPER_AHEADS_LARGE_V2
	case "large-v3", "large":
		return C.WHISPER_AHEADS_LARGE_V3
	case "large-v3-turbo":
		return C.WHISPER_AHEADS_LARGE_V3_TURBO
	}
	return C.WHISPER_AHEADS_N_TOP_MOST
}

// AudioCtxMin — меньше окно не берём: на совсем коротком окне Whisper начинает выдумывать.
var AudioCtxMin = 384

// AudioCtx — сколько окна кодировать. Whisper всегда считает 30 секунд (1500 позиций), даже для фразы
// в одну секунду; для коротких реплик окно по длине звука с запасом ускоряет распознавание в разы.
// 0 — всё окно.
func AudioCtx(samples int) int {
	sec := float64(samples) / SampleRate
	if sec >= 25 || AudioCtxMin <= 0 {
		return 0
	}
	n := (int((sec+1.5)*50) + 63) / 64 * 64 // 50 позиций на секунду, запас полторы секунды, кратно 64
	return min(max(n, AudioCtxMin), 1500)
}

// Transcribe распознаёт моно 16 кГц. lang — код языка («en», «ja») или «auto».
// stop вызывается во время работы: вернёт true — распознавание прервётся.
func (m *Model) Transcribe(samples []float32, lang string, stop func() bool) (string, error) {
	text, err := m.transcribe(samples, lang, AudioCtx(len(samples)), stop)
	// одно короткое слово («Go!», «Ready?») на самом маленьком окне Whisper иногда не слышит — пробуем окно шире
	if err == nil && text == "" {
		if c := AudioCtx(len(samples)); c > 0 && c < retryCtx {
			text, err = m.transcribe(samples, lang, retryCtx, stop)
		}
	}
	return text, err
}

// retryCtx — окно для второй попытки: на нём Ring Fit «Go!», «Perfect!», «Ready?» распознаются.
const retryCtx = 768

func (m *Model) transcribe(samples []float32, lang string, ctx int, stop func() bool) (string, error) {
	segs, err := m.runCtx(samples, lang, false, ctx, stop)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return Clean(b.String()), nil
}

// Segment — кусок речи со временем начала и конца (секунды).
type Segment struct {
	Start, End float64
	Text       string
}

// Segments распознаёт речь кусками со временем — для субтитров и озвучки видео.
// Если осмысленной речи нет совсем (музыка), возвращает пустой список.
func (m *Model) Segments(samples []float32, lang string, stop func() bool) ([]Segment, error) {
	segs, err := m.run(samples, lang, true, stop)
	if err != nil {
		return nil, err
	}
	var all strings.Builder
	var out []Segment
	dur := float64(len(samples)) / SampleRate
	for _, s := range segs {
		s.End = min(s.End, dur)
		s.Start = min(s.Start, s.End)
		all.WriteString(s.Text)
		s.Text = strings.Join(strings.Fields(notesRe.ReplaceAllString(s.Text, " ")), " ")
		if wordRe.MatchString(s.Text) {
			out = append(out, s)
		}
	}
	if Clean(all.String()) == "" {
		return nil, nil
	}
	return out, nil
}

func (m *Model) run(samples []float32, lang string, timestamps bool, stop func() bool) ([]Segment, error) {
	return m.runCtx(samples, lang, timestamps, AudioCtx(len(samples)), stop)
}

func (m *Model) runCtx(samples []float32, lang string, timestamps bool, audioCtx int, stop func() bool) ([]Segment, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	if lang == "" {
		lang = "auto"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return nil, errors.New("модель Whisper закрыта")
	}
	cl := C.CString(lang)
	defer C.free(unsafe.Pointer(cl))
	h := cgo.NewHandle(stop)
	defer h.Delete()
	threads := min(max(runtime.NumCPU()-1, 1), 8)
	ts := C.int(0)
	if timestamps {
		ts = 1
	}
	rc := C.swtr_full(m.ctx, (*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples)), cl, C.int(threads), ts, C.int(audioCtx), unsafe.Pointer(&h))
	if stop != nil && stop() {
		return nil, errors.New("остановлено")
	}
	if rc != 0 {
		return nil, errors.New("Whisper не смог распознать звук")
	}
	var out []Segment
	for i := range int(C.whisper_full_n_segments(m.ctx)) {
		// на музыке и шумах Whisper выдумывает слова — такие куски он сам помечает как «не речь»
		if float32(C.whisper_full_get_segment_no_speech_prob(m.ctx, C.int(i))) > noSpeech {
			continue
		}
		seg := Segment{
			Start: float64(C.whisper_full_get_segment_t0(m.ctx, C.int(i))) / 100,
			End:   float64(C.whisper_full_get_segment_t1(m.ctx, C.int(i))) / 100,
			Text:  C.GoString(C.whisper_full_get_segment_text(m.ctx, C.int(i))),
		}
		if timestamps {
			// границы фразы — по первому и последнему слову: у кусков Whisper они грубые (часто с начала окна)
			eot := C.whisper_token_eot(m.ctx)
			first, last := -1.0, -1.0
			for j := range int(C.whisper_full_n_tokens(m.ctx, C.int(i))) {
				d := C.whisper_full_get_token_data(m.ctx, C.int(i), C.int(j))
				if d.id >= eot || d.t_dtw < 0 {
					continue
				}
				t := float64(d.t_dtw) / 100
				if first < 0 {
					first = t
				}
				last = t
			}
			// t_dtw — момент, когда звучит слово; к последнему добавляем время на само слово
			if first >= seg.Start-0.2 && last >= first {
				seg.Start = max(first-0.1, 0)
				seg.End = min(max(last+0.6, seg.Start+0.8), seg.End)
			}
		}
		out = append(out, seg)
	}
	return out, nil
}

const noSpeech = 0.5

var (
	notesRe = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*|[♪♫♬]`)
	wordRe  = regexp.MustCompile(`\p{L}{2,}`)
)

// Clean убирает пометки вроде [музыка], (смех), ♪ и возвращает "", если осмысленных слов
// не осталось: меньше двух разных слов — обычно это выдумка на музыке («oz oz oz»).
func Clean(text string) string {
	text = strings.Join(strings.Fields(notesRe.ReplaceAllString(text, " ")), " ")
	text = ShortenStretched(text)
	if repetitive(text) {
		return ""
	}
	text = dropRepeats(text)
	words := wordRe.FindAllString(text, -1)
	distinct := map[string]bool{}
	for _, w := range words {
		distinct[strings.ToLower(w)] = true
	}
	switch {
	case len(distinct) == 0:
		return ""
	case len(distinct) == 1 && len(words) > 1:
		return "" // одно слово по кругу («oz oz») — выдумка на музыке
	case len(words) == 1 && fillers[strings.ToLower(words[0])]:
		return "" // одиночное «you», «bye» — частая выдумка Whisper на шуме
	}
	return text
}

// ShortenStretched укорачивает протяжный звук: на долгом крике Whisper пишет «AHHHH…» или «Mmmm…»
// на сотни букв (в Ring Fit — до 800), а переводу и озвучке хватит пяти (XTTS больше 400 знаков в одной фразе не берёт).
func ShortenStretched(text string) string {
	var b strings.Builder
	var prev rune
	run := 0
	for _, r := range text {
		if r == prev && unicode.IsLetter(r) {
			run++
		} else {
			prev, run = r, 1
		}
		if run <= 5 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fillers — слова, которые Whisper «слышит» в шуме и тишине, если больше ничего нет.
var fillers = map[string]bool{"you": true, "bye": true, "so": true, "uh": true, "um": true, "hmm": true, "mm": true, "the": true, "oh": true}

// dropRepeats убирает ту же фразу, повторённую подряд: на коротком звуке Whisper иногда дублирует реплику.
func dropRepeats(text string) string {
	var out []string
	prev := ""
	for _, sent := range sentenceRe.FindAllString(text, -1) {
		key := strings.ToLower(strings.Trim(strings.TrimSpace(sent), ".!?…, "))
		// короткие восклицания («Yes! Yes!») бывают и в игре — схлопываем только фразы от трёх слов
		if key != "" && key == prev && len(wordRe.FindAllString(key, -1)) >= 3 {
			continue
		}
		prev = key
		out = append(out, strings.TrimSpace(sent))
	}
	return strings.Join(out, " ")
}

var sentenceRe = regexp.MustCompile(`[^.!?…]+[.!?…]*`)

// repetitive — одна-две фразы, повторённые по кругу («Thank you. Thank you. Thank you.»):
// так Whisper «слышит» речь в музыке. В настоящей реплике фразы почти не повторяются.
func repetitive(text string) bool {
	var all []string
	uniq := map[string]bool{}
	for _, s := range sentenceRe.FindAllString(text, -1) {
		s = strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!?…, "))
		if s == "" {
			continue
		}
		all = append(all, s)
		uniq[s] = true
	}
	return len(all) >= 3 && len(uniq)*2 <= len(all)
}

// Close освобождает память модели.
func (m *Model) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil {
		C.whisper_free(m.ctx)
		m.ctx = nil
	}
}

//export swtrAbort
func swtrAbort(user unsafe.Pointer) C._Bool {
	stop, _ := (*cgo.Handle)(user).Value().(func() bool)
	return C._Bool(stop != nil && stop())
}
