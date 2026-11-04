// Package project связывает шаги: распаковка → анализ → извлечение → перевод текста и звука.
package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"switch-translator/internal/codec"
	"sync"
	"time"

	"switch-translator/internal/audio"
	"switch-translator/internal/engine"
	"switch-translator/internal/gametext"
	"switch-translator/internal/keys"
	"switch-translator/internal/nx"
	"switch-translator/internal/speech"
	"switch-translator/internal/translate"
	"switch-translator/internal/ttgames"
)

type Logger = nx.Logger

// Progress: done из total, label — что сейчас делается.
type Progress func(label string, done, total float64)

type Session struct {
	Game    *nx.Game
	Report  *engine.Report
	All     []gametext.String // все строки всех языков игры
	Strings []gametext.String // строки выбранного языка-оригинала — их и переводим
	Audio   []AudioLine
	Video   []VideoLine
	Locale  string
	Lang    string // язык оригинала, если в путях игры нет кодов языков Nintendo (см. SelectLang)
	WorkDir string // <выходная папка>/<title id>

	Glossary []Term    // словарь игры для выбранного языка перевода
	Words    WordStats // сколько слов в тексте и речи

	log  Logger
	keys *keys.Keyset

	voiceLocs map[string]bool // чьи архивы озвучки в списке (voiceLocales)

	banksMu   sync.Mutex
	banks     map[string]*audio.FMODBank // оглавления банков FMOD
	sars      map[string]*audio.BFSAR    // оглавления BFSAR
	bnks      map[string]*audio.BNK      // оглавления банков Wwise
	dats      map[string]*ttgames.DAT    // оглавления архивов LEGO
	unwrapped struct {                   // последний распакованный сжатый архив (containerReader)
		path string
		data []byte
	}
}

const (
	maxTextFile  = 64 << 20
	maxUnityFile = 1 << 30 // ассеты Unity с таблицами локализации бывают по 150 МБ
)

// DefaultWorkDir — куда класть результаты: рядом с образом, в папку «<имя образа> — перевод».
// Если туда писать нельзя (образ на защищённом диске), — в ~/Documents/SwitchTranslator.
func DefaultWorkDir(gamePath string) string {
	stem := strings.TrimSuffix(filepath.Base(gamePath), filepath.Ext(gamePath))
	dir := filepath.Join(filepath.Dir(gamePath), stem+" — перевод")
	if writable(dir) {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", "SwitchTranslator", stem)
}

func writable(dir string) bool {
	existed := isDir(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err == nil {
		f.Close()
		os.Remove(f.Name())
	}
	if !existed {
		os.Remove(dir) // создадим заново при анализе; пустую папку заранее не оставляем
	}
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Analyze открывает образ и ищет текст и звук. Результаты пишутся в workDir.
// extras — обновления и DLC: мод должен совпадать с версией на консоли, а тексты DLC тоже переводятся.
func Analyze(gamePath string, extras []string, ks *keys.Keyset, workDir string, log Logger, progress Progress) (*Session, error) {
	tmp := filepath.Join(workDir, ".tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	log("Открываю %s", filepath.Base(gamePath))
	g, err := nx.OpenGame(gamePath, extras, ks, tmp, log, func(done, total int64) {
		progress("Распаковка NCZ", float64(done), float64(total))
	})
	if err != nil {
		return nil, err
	}
	s := &Session{Game: g, WorkDir: workDir, log: log, keys: ks}

	entries := make([]engine.Entry, len(g.RomFS.Files))
	for i, f := range g.RomFS.Files {
		entries[i] = engine.Entry{Path: f.Path, Size: f.Size, R: g.RomFS.Open(f)}
	}
	s.Report = engine.Scan(entries, func(done, total int) { progress("Анализ файлов", float64(done), float64(total)) })
	for _, e := range s.Report.Engines {
		log("Движок: %s — %s", e.Name, e.Reason)
	}
	if len(s.Report.Engines) == 0 {
		log("Движок не определён — смотри таблицу форматов")
	}

	byPath := map[string]nx.RomFile{}
	for _, f := range g.RomFS.Files {
		byPath[f.Path] = f
	}
	var cands []int
	for i, fi := range s.Report.Files {
		limit := int64(maxTextFile)
		if strings.HasPrefix(fi.Format, "Unity") {
			limit = maxUnityFile
		}
		if gametext.MayContainText(fi.Format) && fi.Size <= limit {
			cands = append(cands, i)
		}
	}
	for n, i := range cands {
		fi := &s.Report.Files[i]
		progress("Поиск строк", float64(n+1), float64(len(cands)))
		data, err := io.ReadAll(g.RomFS.Open(byPath[fi.Path]))
		if err != nil {
			log("%s: %v", fi.Path, err)
			continue
		}
		strs, err := gametext.Extract(fi.Path, data)
		if err != nil {
			log("%s: %v", fi.Path, err)
		}
		// архив с репликами внутри — это текст игры
		if len(strs) > 0 && fi.Category != engine.Text {
			fi.Category = engine.Text
			if strings.HasPrefix(fi.Format, "Unity") {
				fi.Format += " с таблицей локализации"
			} else {
				fi.Format += " с MSBT"
			}
		}
		s.All = append(s.All, strs...)
	}
	for i := range s.Report.Files {
		if fi := &s.Report.Files[i]; fi.Format == "Unreal PAK" {
			progress("Поиск строк в .pak", 0, 1)
			if strs := s.pakTexts(*fi); len(strs) > 0 {
				s.All = append(s.All, strs...)
				fi.Category, fi.Format = engine.Text, fi.Format+" с .locres"
			}
		}
	}
	var dats []engine.FileInfo
	for _, fi := range s.Report.Files {
		if fi.Format == "TT Games DAT" {
			dats = append(dats, fi)
		}
	}
	if len(dats) > 0 {
		progress("Поиск строк в .DAT", 0, 1)
		strs := s.datTexts(dats)
		s.All = append(s.All, strs...)
		withText := map[string]bool{}
		for _, st := range strs {
			withText[st.File] = true
		}
		for i := range s.Report.Files {
			if fi := &s.Report.Files[i]; withText[fi.Path] {
				fi.Category, fi.Format = engine.Text, "TT Games DAT с текстом"
			}
		}
	}
	s.Strings = s.All
	if locs := s.Locales(); len(locs) > 1 {
		names := make([]string, len(locs))
		for i, l := range locs {
			names[i] = l.Code
		}
		log("В игре %d языков: %s — переводить нужно один из них", len(locs), strings.Join(names, ", "))
	}
	log("Текст: %d файлов, %d строк; звук: %d файлов",
		len(s.Report.Of(engine.Text)), len(s.All), len(s.Report.Of(engine.Audio)))

	s.initAudio()
	s.initVideo()
	if err := writeJSON(filepath.Join(s.WorkDir, "report.json"), s.Report); err != nil {
		log("report.json: %v", err)
	}
	if len(s.All) > 0 {
		if err := writeJSON(s.stringsPath(""), s.All); err != nil {
			log("strings.json: %v", err)
		}
	}
	return s, nil
}

func (s *Session) Close() {
	s.Game.Close()
	os.Remove(filepath.Join(s.WorkDir, ".tmp")) // только если пустая
}

type LocaleInfo struct {
	Code  string // USen, EUde…
	Lang  string // en, de…
	Count int
}

// Locales — языки, найденные в путях к файлам с текстом.
func (s *Session) Locales() []LocaleInfo {
	count := map[string]int{}
	for _, str := range s.All {
		if str.Locale != "" {
			count[str.Locale]++
		}
	}
	out := make([]LocaleInfo, 0, len(count))
	for c, n := range count {
		out = append(out, LocaleInfo{c, gametext.LocaleLang(c), n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// DefaultLocale подбирает язык-оригинал: сначала совпадающий с lang (US раньше EU),
// иначе английский, иначе первый найденный.
func (s *Session) DefaultLocale(lang string) string {
	locs := s.Locales()
	for _, want := range []string{lang, "en"} {
		for _, prefix := range []string{"US", "EU", ""} { // у таблиц Unity регион в конце: en-US
			for _, l := range locs {
				if l.Lang == want && (strings.HasPrefix(l.Code, prefix) || prefix != "" && strings.HasSuffix(l.Code, "-"+prefix)) {
					return l.Code
				}
			}
		}
	}
	if len(locs) > 0 {
		return locs[0].Code
	}
	return ""
}

// SelectLocale оставляет для перевода строки одного языка; "" — все строки.
// Строки без кода языка в пути (общие файлы) остаются всегда.
func (s *Session) SelectLocale(code string) {
	if s.Report != nil && code != s.Locale {
		defer s.initAudio() // озвучка в архивах — своя у каждого языка
	}
	s.Locale = code
	if code == "" {
		s.Strings = s.All
		return
	}
	s.Strings = nil
	for _, str := range s.All {
		if str.Locale == code || str.Locale == "" {
			s.Strings = append(s.Strings, str)
		}
	}
}

func (s *Session) stringsPath(lang string) string {
	if lang == "" {
		return filepath.Join(s.WorkDir, "strings.json")
	}
	return filepath.Join(s.WorkDir, "strings."+lang+".json")
}

// TextChars — сколько символов уйдёт в перевод (для оценки цены и лимитов).
func (s *Session) TextChars() int {
	n := 0
	for _, str := range s.Strings {
		n += len([]rune(str.Source))
	}
	return n
}

// ExtractFiles копирует в WorkDir/romfs файлы выбранных категорий (nil — все).
func (s *Session) ExtractFiles(cats []engine.Category, progress Progress) (string, error) {
	want := map[string]bool{}
	for _, f := range s.Report.Files {
		for _, c := range cats {
			if f.Category == c {
				want[f.Path] = true
			}
		}
	}
	dir := filepath.Join(s.WorkDir, "romfs")
	var filter func(nx.RomFile) bool
	if cats != nil {
		filter = func(f nx.RomFile) bool { return want[f.Path] }
	}
	err := s.Game.Extract(dir, filter, func(done, total int) { progress("Извлечение", float64(done), float64(total)) })
	return dir, err
}

// TranslationPath — файл перевода для текущего языка-оригинала: strings.USen-ru.json или strings.ru.json.
func (s *Session) TranslationPath(to string) string {
	if s.Locale != "" {
		return s.stringsPath(s.Locale + "-" + to)
	}
	return s.stringsPath(to)
}

// LoadTranslations подтягивает уже сделанный перевод и отметки «не переводить» из файла.
func (s *Session) LoadTranslations(to string) {
	defer s.LoadGlossary(to)
	var done []gametext.String
	b, err := os.ReadFile(s.TranslationPath(to))
	if err != nil || json.Unmarshal(b, &done) != nil {
		return
	}
	prev := map[string]gametext.String{}
	for _, d := range done {
		prev[d.ID+"\x00"+d.Source] = d
	}
	for i := range s.Strings {
		if d, ok := prev[s.Strings[i].ID+"\x00"+s.Strings[i].Source]; ok {
			s.Strings[i].Translation = d.Translation
			s.Strings[i].Skip = d.Skip
		}
	}
}

// SaveTranslations пишет перевод и отметки на диск (после ручной правки или выбора строк).
func (s *Session) SaveTranslations(to string) error {
	return writeJSON(s.TranslationPath(to), s.Strings)
}

// ToTranslate — строки, которые отмечены для перевода и ещё не переведены.
func (s *Session) ToTranslate() []int {
	var todo []int
	for i, str := range s.Strings {
		if !str.Skip && str.Translation == "" {
			todo = append(todo, i)
		}
	}
	return todo
}

// SpeechToTranslate — отмеченные звуковые файлы с распознанной речью без перевода.
func (s *Session) SpeechToTranslate() []int {
	var todo []int
	for i, l := range s.Audio {
		if l.Selected && l.Transcript != "" && l.Translation == "" {
			todo = append(todo, i)
		}
	}
	return todo
}

// target — куда вписать перевод: реплика текста или речь звукового файла.
type target struct {
	audio bool
	i     int
}

func (s *Session) setTranslation(t target, text string) {
	if t.audio {
		s.Audio[t.i].Translation = text
	} else {
		s.Strings[t.i].Translation = text
	}
}

// known — уже готовые переводы по тексту оригинала: из реплик и из речи.
func (s *Session) known() map[string]string {
	k := map[string]string{}
	for _, str := range s.Strings {
		if str.Translation != "" {
			if _, ok := k[str.Source]; !ok {
				k[str.Source] = str.Translation
			}
		}
	}
	for _, l := range s.Audio {
		if l.Transcript != "" && l.Translation != "" {
			if _, ok := k[l.Transcript]; !ok {
				k[l.Transcript] = l.Translation
			}
		}
	}
	return k
}

// TodoTexts — что на самом деле уйдёт переводчику из реплик и речи: одинаковый текст (в играх
// его много — подписи к разным звукам, повторяющиеся подсказки, реплика и её озвучка) переводится
// один раз, а если такой же текст уже переведён, перевод берётся оттуда. groups[k] — куда вписать texts[k].
func (s *Session) TodoTexts() (texts []string, groups [][]target, reused int) {
	return s.todo(s.ToTranslate(), s.SpeechToTranslate())
}

func (s *Session) todo(strs, speech []int) (texts []string, groups [][]target, reused int) {
	done := s.known()
	at := map[string]int{}
	add := func(src string, t target) {
		if _, ok := done[src]; ok {
			reused++
			return
		}
		k, ok := at[src]
		if !ok {
			k = len(texts)
			at[src] = k
			texts = append(texts, src)
			groups = append(groups, nil)
		}
		groups[k] = append(groups[k], t)
	}
	for _, i := range strs {
		add(s.Strings[i].Source, target{i: i})
	}
	for _, i := range speech {
		add(s.Audio[i].Transcript, target{audio: true, i: i})
	}
	return texts, groups, reused
}

// fillRepeats дописывает готовый перевод в непереведённые реплики и речь с тем же текстом.
func (s *Session) fillRepeats(strs, speech []int) int {
	known := s.known()
	n := 0
	for _, i := range strs {
		if t, ok := known[s.Strings[i].Source]; ok {
			s.Strings[i].Translation = t
			n++
		}
	}
	for _, i := range speech {
		if t, ok := known[s.Audio[i].Transcript]; ok {
			s.Audio[i].Translation = t
			n++
		}
	}
	return n
}

// TranslateText переводит отмеченные реплики и распознанную речь пачками и сохраняет результат после
// каждой пачки, поэтому прерванный перевод продолжится с места остановки. Готовые переводы и ручные
// правки не трогает. Одинаковый текст переводится один раз и вписывается всюду, где он есть.
func (s *Session) TranslateText(ctx context.Context, tr translate.Translator, to string, progress Progress) (string, error) {
	out := s.TranslationPath(to)
	err := s.translate(ctx, tr, s.ToTranslate(), s.SpeechToTranslate(), "Перевод текста", func() error {
		if err := writeJSON(out, s.Strings); err != nil {
			return err
		}
		return s.SaveAudio()
	}, progress)
	return out, err
}

// TranslateAudio переводит распознанную речь выбранных файлов (уже переведённую не трогает).
func (s *Session) TranslateAudio(ctx context.Context, tr translate.Translator, idx []int, progress Progress) error {
	var todo []int
	for _, i := range idx {
		if l := s.Audio[i]; l.Transcript != "" && l.Translation == "" {
			todo = append(todo, i)
		}
	}
	return s.translate(ctx, tr, nil, todo, "Перевод речи", s.SaveAudio, progress)
}

func (s *Session) translate(ctx context.Context, tr translate.Translator, strs, speech []int, label string, save func() error, progress Progress) error {
	if n := s.fillRepeats(strs, speech); n > 0 {
		s.log("Повторы уже переведённого: %d — перевод взят оттуда", n)
		strs, speech = s.untranslated(strs, speech)
	}
	texts, groups, _ := s.todo(strs, speech)
	if total := repeatCount(groups); total > 0 {
		s.log("Разных текстов к переводу: %d, ещё %d повторяют их — переводчику они не отправляются", len(texts), total)
	}
	if len(speech) > 0 && len(strs) > 0 {
		s.log("Вместе с текстом переводится распознанная речь: %d файлов", len(speech))
	}
	if g := len(s.GlossaryTerms()); g > 0 {
		s.log("Переводчик сверяется со словарём: %d терминов", g)
	}
	// пачки идут в несколько потоков: у ИИ-переводчиков почти всё время — ожидание ответа
	bs := tr.BatchSize()
	var starts []int
	for start := 0; start < len(texts); start += bs {
		starts = append(starts, start)
	}
	workers := min(translate.Workers(tr), len(starts))
	if len(starts) > 1 {
		s.log("Перевожу %d пачек по %d текстов, по %d одновременно. Первые пачки будут готовы через пару минут — "+
			"после каждой перевод сохраняется и здесь появляется строка", len(starts), bs, workers)
	}
	if len(starts) > 0 {
		progress(label, 0, float64(len(texts)))
	}
	began := time.Now()
	jobs := make(chan int)
	stop := make(chan struct{}) // после ошибки новые пачки не начинаются, начатые доводятся до конца
	var (
		mu       sync.Mutex
		firstErr error
		done     int
		wg       sync.WaitGroup
		once     sync.Once
	)
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
		once.Do(func() { close(stop) })
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for start := range jobs {
				end := min(start+bs, len(texts))
				t0 := time.Now()
				res, err := translateRetry(ctx, tr, texts[start:end], func(wait time.Duration, err error) {
					s.log("Пачка %d–%d: %v — повторю через %s", start+1, end, err, wait)
				})
				mu.Lock()
				if err != nil {
					if ctx.Err() == nil {
						fail(fmt.Errorf("тексты %d–%d: %w", start+1, end, err))
					}
					mu.Unlock()
					continue
				}
				for j, t := range res {
					for _, g := range groups[start+j] {
						s.setTranslation(g, t)
					}
				}
				done += end - start
				left := time.Duration(float64(time.Since(began)) / float64(done) * float64(len(texts)-done))
				if len(starts) > 1 {
					s.log("Пачка %d–%d готова за %s · переведено %d из %d · осталось примерно %s",
						start+1, end, time.Since(t0).Round(time.Second), done, len(texts), left.Round(time.Minute))
				}
				if err := save(); err != nil {
					fail(err)
				}
				progress(label, float64(done), float64(len(texts)))
				mu.Unlock()
			}
		}()
	}
feed:
	for _, start := range starts {
		select {
		case jobs <- start:
		case <-stop:
			break feed
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return save()
}

// untranslated — то из strs и speech, что всё ещё без перевода.
func (s *Session) untranslated(strs, speech []int) ([]int, []int) {
	var a, b []int
	for _, i := range strs {
		if s.Strings[i].Translation == "" {
			a = append(a, i)
		}
	}
	for _, i := range speech {
		if s.Audio[i].Translation == "" {
			b = append(b, i)
		}
	}
	return a, b
}

// translateRetry переводит пачку, а при сбое связи или перегрузке сервиса пробует ещё дважды:
// долгие запросы иногда обрываются по дороге, и терять из-за этого весь перевод обидно.
func translateRetry(ctx context.Context, tr translate.Translator, texts []string, onRetry func(time.Duration, error)) ([]string, error) {
	return withRetries(ctx, func() ([]string, error) { return tr.Translate(ctx, texts) }, onRetry)
}

func withRetries(ctx context.Context, fn func() ([]string, error), onRetry func(time.Duration, error)) ([]string, error) {
	waits := retryWaits
	for attempt := 0; ; attempt++ {
		res, err := fn()
		if err == nil || ctx.Err() != nil || attempt >= len(waits) || !retryable(err) {
			return res, err
		}
		onRetry(waits[attempt], err)
		select {
		case <-time.After(waits[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

var retryWaits = []time.Duration{15 * time.Second, 45 * time.Second}

// retryable — ошибка, которая обычно проходит сама: обрыв связи, перегрузка, лимит запросов.
// Не повторяем то, что само не исправится: нет входа в аккаунт, нет ключа.
func retryable(err error) bool {
	m := strings.ToLower(err.Error())
	for _, s := range []string{"не вошёл", "api-ключ", "401", "403", "invalid api key", "authentication"} {
		if strings.Contains(m, s) {
			return false
		}
	}
	return true
}

func repeatCount(groups [][]target) int {
	n := 0
	for _, g := range groups {
		n += len(g) - 1
	}
	return n
}

// AudioLine — звуковой файл игры и что с ним сделано.
type AudioLine struct {
	File        string `json:"file"`
	Size        int64  `json:"size"`
	Format      string `json:"format"`
	Selected    bool   `json:"selected"` // отмечен для распознавания и переозвучки
	Recognized  bool   `json:"recognized,omitempty"`
	Transcript  string `json:"transcript,omitempty"`
	Translation string `json:"translation,omitempty"`
	Dub         string `json:"dub,omitempty"`
	Error       string `json:"error,omitempty"`
	Guess       string `json:"guess,omitempty"`   // без Whisper: «голос», «музыка», «эффект», «тишина»
	Speaker     string `json:"speaker,omitempty"` // чей голос: «Голос 1» после разделения по голосам или имя персонажа
	// сделано вручную — автоматические шаги этого не перезаписывают
	HandText        bool `json:"handText,omitempty"`        // текст речи вписан или исправлен руками
	HandTranslation bool `json:"handTranslation,omitempty"` // перевод вписан или исправлен руками
	HandDub         bool `json:"handDub,omitempty"`         // своя запись или свой файл озвучки
	HandSpeaker     bool `json:"handSpeaker,omitempty"`     // голос персонажа указан руками
}

func (s *Session) audioPath() string { return filepath.Join(s.WorkDir, "audio.json") }

// initAudio собирает список звука из отчёта и подтягивает прошлую работу из audio.json.
// Звуковые архивы BFSAR сюда не попадают: это сотни коротких звуков в одном файле.
func (s *Session) initAudio() {
	prev := map[string]AudioLine{}
	var saved []AudioLine
	if b, err := os.ReadFile(s.audioPath()); err == nil && json.Unmarshal(b, &saved) == nil {
		for _, l := range saved {
			prev[l.File] = l
		}
	}
	s.Audio = nil
	s.voiceLocs = s.voiceLocales()
	files := s.Report.Of(engine.Audio)
	otherLang := s.otherLangBanks(files)
	for _, f := range files {
		if otherLang[f.Path] {
			continue
		}
		// звук с кодом языка в пути — только выбранного языка (и версии, чью озвучку он берёт)
		if loc := gametext.Locale(f.Path); loc != "" && s.Locale != "" && !s.voiceLocs[loc] {
			continue
		}
		add := func(file, format string, size int64) {
			line, ok := prev[file]
			if !ok {
				line = AudioLine{File: file}
			}
			line.Size, line.Format = size, format
			s.Audio = append(s.Audio, line)
		}
		// банк FMOD — каждым звуком отдельно; банк без звуков (Master.strings.bank) не показываем
		if strings.HasPrefix(f.Format, "FMOD") {
			s.addBankSounds(f, add)
			continue
		}
		// BFSAR и банки Wwise — каждым звуком; без звуков внутри (Init.bnk) не показываем
		if strings.HasPrefix(f.Format, "BFSAR") || strings.HasPrefix(f.Format, "Wwise SoundBank") {
			s.addContainerSounds(f, add)
			continue
		}
		// архив BARS — каждым звуком отдельно; у заготовок музыки (prefetch) в архиве только начало,
		// вся музыка лежит отдельным файлом в Stream/ — так и подписываем
		if strings.HasPrefix(f.Format, "BARS") {
			if data, err := s.readRom(f.Path); err == nil {
				data, _, _ = codec.Unwrap(data) // в Mario Wonder архивы сжаты zstd: *.bars.zs
				if a, err := audio.ParseBARS(data); err == nil {
					n, fstp := 0, 0
					for _, e := range a.Entries {
						if e.Name == "" {
							continue
						}
						if bytes.HasPrefix(e.BWAV, []byte("FSTP")) {
							fstp++ // начало музыки старого вида (Mario Kart 8): сама музыка — отдельным BFSTM, он уже в списке
							continue
						}
						format := string(e.BWAV[:min(4, len(e.BWAV))]) + " в архиве BARS"
						if len(e.BWAV) == 0 {
							format = "BWAV в архиве BARS" // общий с другим звуком
						}
						if e.Prefetch {
							format = "начало музыки (целиком — в Stream/" + e.Name + ".bwav)"
						}
						add(f.Path+barsSep+e.Name, format, int64(len(e.BWAV)))
						n++
					}
					if n > 0 || fstp > 0 {
						continue
					}
				}
			}
		}
		add(f.Path, f.Format, f.Size)
	}
	// озвучка внутри архивов SARC (Ring Fit: Pack/Voice.USen.pack → Voice/USen/BtlVoice.bars → реплики)
	for _, f := range s.Report.Files {
		if !strings.HasPrefix(f.Format, "SARC") || !s.voiceArchive(f) {
			continue
		}
		data, err := s.readRom(f.Path)
		if err != nil {
			continue
		}
		files, err := gametext.ArchiveFiles(data)
		if err != nil {
			continue
		}
		for _, in := range files {
			raw, _, err := codec.Unwrap(in.Data)
			if err != nil || len(raw) < 4 {
				continue
			}
			inner := f.Path + archSep + in.Name
			switch {
			case string(raw[:4]) == "BARS":
				a, err := audio.ParseBARS(raw)
				if err != nil {
					continue
				}
				for _, e := range a.Entries {
					if e.Name == "" || e.Prefetch {
						continue
					}
					line, ok := prev[inner+barsSep+e.Name]
					if !ok {
						line = AudioLine{File: inner + barsSep + e.Name}
					}
					line.Size, line.Format = int64(len(e.BWAV)), "реплика в архиве "+path.Base(in.Name)
					s.Audio = append(s.Audio, line)
				}
			case string(raw[:4]) == "FGRP":
				// группа звукового архива (Captain Toad: SoundData/SeResourceStd2nd.sarc → *.bfgrp)
				for _, w := range audio.FGRPWaves(raw) {
					line, ok := prev[inner+barsSep+w.Name]
					if !ok {
						line = AudioLine{File: inner + barsSep + w.Name}
					}
					line.Size, line.Format = w.Size(), "звук в группе "+path.Base(in.Name)
					s.Audio = append(s.Audio, line)
				}
			case audio.Supported(raw):
				line, ok := prev[inner]
				if !ok {
					line = AudioLine{File: inner}
				}
				line.Size, line.Format = int64(len(raw)), string(raw[:4])+" в архиве"
				s.Audio = append(s.Audio, line)
			}
		}
	}
	addLine := func(file, format string, size int64) {
		line, ok := prev[file]
		if !ok {
			line = AudioLine{File: file}
		}
		line.Size, line.Format = size, format
		s.Audio = append(s.Audio, line)
	}
	s.addDATSounds(addLine)    // реплики в архивах LEGO
	s.addBundleSounds(addLine) // звук в бандлах Unity
	sort.Slice(s.Audio, func(i, j int) bool { return s.Audio[i].File < s.Audio[j].File })
	s.fixLoops()
}

// fixLoops чинит уже распознанные строки, где Whisper «заело» (одна фраза десятки раз подряд):
// остаётся одна фраза, а перевод и озвучка, сделанные не вручную, сбрасываются — их надо сделать заново.
func (s *Session) fixLoops() {
	n, stretched := 0, 0
	for i := range s.Audio {
		l := &s.Audio[i]
		if l.HandText || l.Transcript == "" {
			continue
		}
		// протяжный крик на сотни букв укорачиваем и в переводе — переводить заново не нужно
		if short := speech.ShortenStretched(l.Transcript); short != l.Transcript {
			l.Transcript = short
			if !l.HandTranslation {
				l.Translation = speech.ShortenStretched(l.Translation)
			}
			if !l.HandDub {
				l.Dub = ""
			}
			stretched++
			s.log("%s: протяжный звук укорочен — «%s»", path.Base(l.File), short)
		}
		clean, changed := speech.CleanTranscript(l.Transcript)
		if !changed && s.Game != nil {
			// удвоенная короткая фраза: судим по длине звука, а её узнаём, только если текст — точный повтор
			if _, maybe := speech.CollapseDouble(l.Transcript, 0.01); maybe {
				if p, err := s.decodeAudio(context.Background(), l.File); err == nil {
					clean, changed = speech.CollapseDouble(l.Transcript, p.Seconds())
				}
			}
		}
		if !changed {
			continue
		}
		l.Transcript = clean
		if !l.HandTranslation {
			l.Translation = ""
		}
		if !l.HandDub {
			l.Dub = ""
		}
		n++
		s.log("%s: распознавание повторило фразу — оставлено «%s»", path.Base(l.File), clean)
	}
	if n > 0 {
		s.log("Исправлено заевших распознаваний: %d — переведи и озвучь их заново", n)
	}
	if stretched > 0 {
		s.log("Укорочено протяжных звуков: %d — озвучь их заново", stretched)
	}
	if n > 0 || stretched > 0 {
		if err := s.SaveAudio(); err != nil {
			s.log("audio.json: %v", err)
		}
	}
}

// archSep отделяет файл внутри архива SARC: «Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars».
const archSep = "::"

// voiceArchive — заглядывать ли в архив за озвучкой: архив выбранного языка (в пути код вроде USen),
// с «voice» в имени или с группами звукового архива (.bfgrp). Озвучки всех языков разом не показываем — их бывают десятки тысяч реплик.
func (s *Session) voiceArchive(f engine.FileInfo) bool {
	if loc := gametext.Locale(f.Path); loc != "" {
		return s.Locale != "" && s.voiceLocs[loc]
	}
	return strings.Contains(strings.ToLower(f.Path), "voice") || f.Inner[".bfgrp"] > 0
}

// voiceLocales — чьи архивы озвучки показывать: выбранной версии, а если своей озвучки у неё меньше, чем у другой
// версии того же языка, — и той (в Ring Fit у EUen и CNzh озвучки нет — игра берёт USen и TWzh; у USfr — несколько реплик, остальное в EUfr).
func (s *Session) voiceLocales() map[string]bool {
	out := map[string]bool{}
	if s.Locale == "" {
		return out
	}
	out[s.Locale] = true
	count := map[string]int{}
	for _, f := range s.Report.Files {
		loc := gametext.Locale(f.Path)
		if loc == "" {
			continue
		}
		if f.Category == engine.Audio { // Mario Wonder: озвучка — отдельные BWAV в папках языков
			count[loc]++
			continue
		}
		if !strings.HasPrefix(f.Format, "SARC") {
			continue
		}
		for ext, n := range f.Inner {
			switch strings.ToLower(ext) {
			case ".bars", ".bwav", ".bfstm", ".bfwav":
				count[loc] += n
			}
		}
	}
	lang, best := gametext.LocaleLang(s.Locale), ""
	for loc, n := range count {
		if loc != s.Locale && gametext.LocaleLang(loc) == lang && n > count[s.Locale] && (best == "" || n > count[best] || n == count[best] && loc < best) {
			best = loc
		}
	}
	if best == "" && count[s.Locale] == 0 {
		// своей озвучки нет и у других версий этого языка (польская версия Mario Wonder) — игра играет английскую
		for _, loc := range []string{s.Locale[:2] + "en", "USen", "EUen"} {
			if count[loc] > 0 {
				best = loc
				break
			}
		}
	}
	if best != "" {
		out[best] = true
	}
	return out
}

// readFile — файл игры или файл внутри архива («архив::путь»), уже распакованный из Yaz0/zstd.
func (s *Session) readFile(p string) ([]byte, error) {
	outer, inner, nested := strings.Cut(p, archSep)
	data, err := s.readRom(outer)
	if err != nil || !nested {
		return data, err
	}
	files, err := gametext.ArchiveFiles(data)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Name == inner {
			raw, _, err := codec.Unwrap(f.Data)
			return raw, err
		}
	}
	return nil, fmt.Errorf("в %s нет файла %s", outer, inner)
}

// barsSep отделяет имя звука от архива: «Voice/VoicePreview.bars#PreviewVoice_USen».
const barsSep = "#"

// splitBARS — звук внутри архива BARS: путь архива и имя звука.
func splitBARS(file string) (archive, name string, ok bool) {
	archive, name, ok = strings.Cut(file, barsSep)
	low := strings.ToLower(archive)
	return archive, name, ok && (strings.HasSuffix(low, ".bars") || strings.HasSuffix(low, ".bars.zs"))
}

// splitFGRP — звук группы BFGRP внутри архива SARC: «архив::группа.bfgrp» и имя звука.
func splitFGRP(file string) (group, name string, ok bool) {
	group, name, ok = strings.Cut(file, barsSep)
	return group, name, ok && strings.HasSuffix(strings.ToLower(group), ".bfgrp")
}

// audioData — байты звука: файл игры, звук BWAV из архива BARS или звук банка FMOD (отдельным FSB5).
func (s *Session) audioData(file string) ([]byte, error) {
	if dat, name, ok := splitDATSound(file); ok {
		return s.datSound(dat, name)
	}
	if bank, name, ok := splitBank(file); ok {
		return s.bankSound(bank, name)
	}
	if data, ok, err := s.containerSound(file); ok {
		return data, err
	}
	if group, name, ok := splitFGRP(file); ok {
		data, err := s.readFile(group)
		if err != nil {
			return nil, err
		}
		return audio.FGRPWave(data, name)
	}
	archive, name, ok := splitBARS(file)
	if !ok {
		return s.readFile(file)
	}
	data, err := s.readFile(archive)
	if err != nil {
		return nil, err
	}
	if data, _, err = codec.Unwrap(data); err != nil {
		return nil, err
	}
	a, err := audio.ParseBARS(data)
	if err != nil {
		return nil, err
	}
	i, found := a.Find(name)
	if !found {
		return nil, fmt.Errorf("в %s нет звука %s", archive, name)
	}
	return a.Sound(i), nil
}

// В файле хранится работа со всеми языками озвучки: файлы, которых нет в текущем списке
// (озвучка другого языка), переписываются из файла как были.
func (s *Session) SaveAudio() error {
	all := append([]AudioLine(nil), s.Audio...)
	var saved []AudioLine
	if b, err := os.ReadFile(s.audioPath()); err == nil && json.Unmarshal(b, &saved) == nil {
		cur := make(map[string]bool, len(s.Audio))
		for _, l := range s.Audio {
			cur[l.File] = true
		}
		for _, l := range saved {
			if !cur[l.File] {
				all = append(all, l)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].File < all[j].File })
	return writeJSON(s.audioPath(), all)
}

// audioBase — имя для временных файлов звука; «:» и «#» в путях архивов в именах файлов Windows нельзя.
// У звука из архива («…Training.bars#Trg_Go00») имя звука — после «#», и расширение не отрезается:
// иначе все звуки одного архива получили бы одно имя файла.
func (s *Session) audioBase(file string) string {
	if !strings.Contains(file, barsSep) {
		file = strings.TrimSuffix(file, path.Ext(file))
	}
	return strings.NewReplacer("/", "__", ":", "_", "#", "~").Replace(file)
}

// AudioWav декодирует звуковой файл игры в WAV (Nintendo — своим кодом, остальное — vgmstream) и кэширует результат.
func (s *Session) AudioWav(ctx context.Context, file string) (string, error) {
	dir := filepath.Join(s.WorkDir, "audio", "wav")
	wav := filepath.Join(dir, s.audioBase(file)+".wav")
	if fileExists(wav) {
		return wav, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := s.audioData(file)
	if err != nil {
		return "", err
	}
	ext := path.Ext(file)
	if _, _, ok := splitBARS(file); ok {
		ext = ".bwav"
	} else if _, _, ok := splitBank(file); ok {
		ext = ".fsb"
	} else if _, _, ok := splitSAR(file); ok {
		ext = ".bfwav"
	} else if _, _, ok := splitFGRP(file); ok {
		ext = ".bfwav"
	} else if _, _, ok := splitBNK(file); ok {
		ext = ".wem"
	}
	raw := filepath.Join(dir, s.audioBase(file)+ext)
	if err := os.WriteFile(raw, data, 0o644); err != nil {
		return "", err
	}
	defer os.Remove(raw)
	if err := speech.DecodeToWav(ctx, raw, wav); err != nil {
		return "", err
	}
	return wav, nil
}

// eachAudio проходит по отмеченным файлам, сохраняя audio.json после каждого.
func (s *Session) eachAudio(ctx context.Context, label string, idx []int, progress Progress, fn func(l *AudioLine) error) error {
	began := time.Now()
	for n, i := range idx {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress(label+Remaining(began, n, len(idx)), float64(n), float64(len(idx)))
		l := &s.Audio[i]
		l.Error = ""
		if err := fn(l); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			l.Error = err.Error()
			s.log("%s: %v", path.Base(l.File), err)
		}
		if err := s.SaveAudio(); err != nil {
			return err
		}
	}
	progress(label, float64(len(idx)), float64(len(idx)))
	return nil
}

// GuessVoices без Whisper прикидывает, в каких файлах голос, а где музыка и эффекты (по огибающей
// громкости). Секунды на всю игру — чтобы потом распознавать только файлы с голосом.
func (s *Session) GuessVoices(ctx context.Context, idx []int, progress Progress) error {
	began := time.Now()
	for n, i := range idx {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n%20 == 0 {
			progress("Поиск голоса"+Remaining(began, n, len(idx)), float64(n), float64(len(idx)))
		}
		l := &s.Audio[i]
		p, err := s.decodeAudio(ctx, l.File)
		if err != nil {
			l.Guess = ""
			continue
		}
		l.Guess, _ = audio.Features(p).Voice()
	}
	progress("Поиск голоса", float64(len(idx)), float64(len(idx)))
	return s.SaveAudio()
}

// decodeAudio — звук в памяти: форматы Nintendo своим кодом, остальное — через WAV (ffmpeg, vgmstream).
func (s *Session) decodeAudio(ctx context.Context, file string) (*audio.PCM, error) {
	data, err := s.audioData(file)
	if err != nil {
		return nil, err
	}
	if audio.Supported(data) {
		return audio.Decode(data)
	}
	wav, err := s.AudioWav(ctx, file)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		return nil, err
	}
	return audio.ReadWAV(b)
}

// Recognize распознаёт речь в выбранных файлах. Пустая расшифровка — значит, там музыка или эффекты.
func (s *Session) Recognize(ctx context.Context, stt speech.Recognizer, idx []int, progress Progress) error {
	var todo []int
	for _, i := range idx {
		if !s.Audio[i].HandText {
			todo = append(todo, i)
		}
	}
	if n := len(idx) - len(todo); n > 0 {
		s.log("Файлов с текстом, вписанным вручную: %d — их не распознаю заново", n)
	}
	return s.eachAudio(ctx, "Распознавание речи", todo, progress, func(l *AudioLine) error {
		wav, err := s.AudioWav(ctx, l.File)
		if err != nil {
			return err
		}
		text, err := stt.Transcribe(ctx, wav)
		if err != nil {
			return err
		}
		text, _ = speech.CleanTranscript(strings.TrimSpace(text))
		if b, err := os.ReadFile(wav); err == nil {
			if p, err := audio.ReadWAV(b); err == nil {
				text, _ = speech.CollapseDouble(text, p.Seconds())
			}
		}
		l.Recognized, l.Transcript = true, text
		if l.Transcript == "" {
			s.log("%s: речи нет", path.Base(l.File))
		} else {
			s.log("%s: %s", path.Base(l.File), l.Transcript)
		}
		return nil
	})
}

// Dub озвучивает перевод выбранных файлов.
func (s *Session) Dub(ctx context.Context, tts speech.Synthesizer, idx []int, progress Progress) error {
	return s.dubLines(ctx, tts, idx, "Озвучка", progress)
}

func (s *Session) dubLines(ctx context.Context, tts speech.Synthesizer, idx []int, label string, progress Progress) error {
	dir := filepath.Join(s.WorkDir, "audio", "dub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var todo []int
	for _, i := range idx {
		if !s.Audio[i].HandDub {
			todo = append(todo, i)
		}
	}
	if n := len(idx) - len(todo); n > 0 {
		s.log("Файлов со своей озвучкой: %d — их не озвучиваю голосом", n)
	}
	return s.eachAudio(ctx, label, todo, progress, func(l *AudioLine) error {
		if l.Translation == "" {
			return nil
		}
		out, err := tts.Synthesize(ctx, speech.ShortenStretched(l.Translation), filepath.Join(dir, s.audioBase(l.File)))
		if err != nil {
			return err
		}
		l.Dub = out
		return nil
	})
}

// Remaining — «, файл 12 из 300, осталось ~8 мин» по уже сделанному; пока сделано мало, только номер.
func Remaining(began time.Time, done, total int) string {
	out := fmt.Sprintf(" (%d из %d", min(done+1, total), total)
	if el := time.Since(began); done >= 3 || (done >= 1 && el > 20*time.Second) {
		left := time.Duration(float64(el) / float64(done) * float64(total-done))
		out += ", осталось " + roughDuration(left)
	}
	return out + ")"
}

func roughDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "меньше минуты"
	case d < time.Hour:
		return fmt.Sprintf("~%d мин", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("~%d ч %02d мин", int(d.Hours()), int(d.Minutes())%60)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func copyOut(r io.Reader, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
