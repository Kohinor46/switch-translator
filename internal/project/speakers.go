package project

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"switch-translator/internal/audio"
	"switch-translator/internal/speech"
)

// Голоса персонажей. Реплики с речью делятся по голосам: у каждой считается отпечаток голоса,
// похожие собираются в группы «Голос 1», «Голос 2»… (первый — у кого больше всего реплик).
// Голос можно переименовать в персонажа и поправить у отдельной реплики руками. При озвучке
// каждому голосу — свой голос озвучки: скопированный по его же репликам или выбранный руками.

// voicePrint — отпечаток голоса реплики и длина речи в ней (кэш: пересчитывать его долго).
type voicePrint struct {
	Print   []float32
	Seconds float64
}

func (s *Session) printsPath() string { return filepath.Join(s.WorkDir, "audio", "voiceprints.gob") }

func (s *Session) loadPrints() map[string]voicePrint {
	out := map[string]voicePrint{}
	if f, err := os.Open(s.printsPath()); err == nil {
		gob.NewDecoder(f).Decode(&out)
		f.Close()
	}
	return out
}

func (s *Session) savePrints(m map[string]voicePrint) error {
	if err := os.MkdirAll(filepath.Dir(s.printsPath()), 0o755); err != nil {
		return err
	}
	tmp := s.printsPath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = gob.NewEncoder(f).Encode(m)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.printsPath())
}

// speechPart — речь для отпечатка: моно 16 кГц без тишины по краям, не длиннее 20 секунд.
func speechPart(p *audio.PCM) []float32 {
	m := audio.Mono16k(p)
	const quiet = 0.01 // −40 дБ
	a, b := 0, len(m)
	for a < b && m[a] < quiet && m[a] > -quiet {
		a++
	}
	for b > a && m[b-1] < quiet && m[b-1] > -quiet {
		b--
	}
	return m[a:min(b, a+20*16000)]
}

const autoVoice = "Голос "

// SplitVoices делит реплики с речью из idx по голосам. want > 0 — сколько голосов в игре, если известно.
// Голос, указанный руками, не меняется, а имена прошлых голосов (и переименованных в персонажей)
// переходят к новым группам, где они у большинства реплик.
func (s *Session) SplitVoices(ctx context.Context, idx []int, want int, progress Progress) error {
	var todo []int
	for _, i := range idx {
		if l := s.Audio[i]; l.Recognized && l.Transcript != "" {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return fmt.Errorf("среди отмеченных нет распознанной речи — сначала нажми «Распознать речь в звуке»")
	}
	cache := s.loadPrints()
	var have []int
	var prints [][]float32
	var weight []float64
	began, fresh := time.Now(), 0
	for n, i := range todo {
		if err := ctx.Err(); err != nil {
			s.savePrints(cache)
			return err
		}
		progress("Отпечатки голосов"+Remaining(began, n, len(todo)), float64(n), float64(len(todo)))
		file := s.Audio[i].File
		vp, ok := cache[file]
		if !ok {
			p, err := s.decodeAudio(ctx, file)
			if err != nil {
				s.log("%s: %v", path.Base(file), err)
				continue
			}
			part := speechPart(p)
			vp.Seconds = float64(len(part)) / 16000
			if vp.Seconds >= 0.3 {
				if vp.Print, err = speech.VoicePrint(ctx, part); err != nil {
					if ctx.Err() != nil {
						s.savePrints(cache)
						return ctx.Err()
					}
					return err
				}
			}
			cache[file] = vp
			if fresh++; fresh%200 == 0 {
				s.savePrints(cache) // прервут — посчитанное не пропадёт
			}
		}
		if vp.Print != nil {
			have = append(have, i)
			prints = append(prints, vp.Print)
			weight = append(weight, min(vp.Seconds, 10))
		}
	}
	if err := s.savePrints(cache); err != nil {
		return err
	}
	progress("Деление по голосам", float64(len(todo)), float64(len(todo)))
	labels := speech.ClusterVoices(prints, weight, want, speech.VoiceThreshold)

	k := 0
	for _, l := range labels {
		k = max(k, l+1)
	}
	// прежние имена в каждой группе
	votes := make([]map[string]int, k)
	members := make([]int, k)
	for j, i := range have {
		if l := labels[j]; l >= 0 {
			members[l]++
			if name := s.Audio[i].Speaker; name != "" {
				if votes[l] == nil {
					votes[l] = map[string]int{}
				}
				votes[l][name]++
			}
		}
	}
	inSplit := map[int]bool{}
	for _, i := range todo {
		inSplit[i] = true
	}
	used := map[string]bool{} // имена, оставшиеся у реплик вне деления и у указанных руками
	for i, l := range s.Audio {
		if l.Speaker != "" && (!inSplit[i] || l.HandSpeaker) {
			used[l.Speaker] = true
		}
	}
	names := make([]string, k)
	for l := range k { // группы уже по убыванию веса: большие забирают имя первыми
		best, bestN := "", 0
		for name, n := range votes[l] {
			if n > bestN || n == bestN && name < best {
				best, bestN = name, n
			}
		}
		if best != "" && bestN*2 > members[l] && !taken(names, best) {
			names[l] = best
		}
	}
	next := 1
	for l := range k {
		for names[l] == "" {
			if name := fmt.Sprintf("%s%d", autoVoice, next); !used[name] && !taken(names, name) {
				names[l] = name
			}
			next++
		}
	}
	for _, i := range todo {
		if !s.Audio[i].HandSpeaker {
			s.Audio[i].Speaker = ""
		}
	}
	for j, i := range have {
		if l := labels[j]; l >= 0 && !s.Audio[i].HandSpeaker {
			s.Audio[i].Speaker = names[l]
		}
	}
	none := 0
	for _, i := range todo {
		if s.Audio[i].Speaker == "" {
			none++
		}
	}
	s.log("Голосов: %d в %d репликах", k, len(todo)-none)
	for _, sp := range s.Speakers() {
		s.log("  %s — %d реплик", sp.Name, sp.Lines)
	}
	if none > 0 {
		s.log("Без голоса: %d реплик — слишком короткие или ни на кого не похожи; их озвучит голос по умолчанию", none)
	}
	return s.SaveAudio()
}

func taken(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// SpeakerInfo — голос персонажа и сколько у него реплик.
type SpeakerInfo struct {
	Name  string
	Lines int
}

// Speakers — голоса в списке звука, от самого частого.
func (s *Session) Speakers() []SpeakerInfo {
	count := map[string]int{}
	for _, l := range s.Audio {
		if l.Speaker != "" {
			count[l.Speaker]++
		}
	}
	out := make([]SpeakerInfo, 0, len(count))
	for name, n := range count {
		out = append(out, SpeakerInfo{name, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SetSpeaker — голос реплики i указан руками ("" — голос не определён).
func (s *Session) SetSpeaker(i int, name string) error {
	l := &s.Audio[i]
	name = strings.TrimSpace(name)
	if name == l.Speaker {
		return nil
	}
	l.Speaker, l.HandSpeaker = name, true
	if !l.HandDub {
		l.Dub = "" // другой голос — озвучить заново
	}
	return s.SaveAudio()
}

// RenameSpeaker переименовывает голос (например, «Голос 1» → «Ринг»). Имя другого голоса — слить их в один.
func (s *Session) RenameSpeaker(old, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == old {
		return nil
	}
	for i := range s.Audio {
		if s.Audio[i].Speaker == old {
			s.Audio[i].Speaker = name
		}
	}
	v := s.loadVoices()
	if v[old] != nil {
		if v[name] == nil {
			v[name] = v[old]
		}
		delete(v, old)
		if err := writeJSON(s.voicesPath(), v); err != nil {
			return err
		}
	}
	return s.SaveAudio()
}

// голоса озвучки персонажей: голос → озвучка → голос озвучки (подпись из списка голосов)
type speakerVoices map[string]map[string]string

func (s *Session) voicesPath() string { return filepath.Join(s.WorkDir, "voices.json") }

func (s *Session) loadVoices() speakerVoices {
	v := speakerVoices{}
	if b, err := os.ReadFile(s.voicesPath()); err == nil {
		json.Unmarshal(b, &v)
	}
	return v
}

// SpeakerVoice — голос озвучки tts для голоса персонажа name ("" — не выбран).
func (s *Session) SpeakerVoice(name, tts string) string { return s.loadVoices()[name][tts] }

// SetSpeakerVoice выбирает голос озвучки для персонажа ("" — голос по умолчанию).
func (s *Session) SetSpeakerVoice(name, tts, voice string) error {
	v := s.loadVoices()
	if v[name] == nil {
		v[name] = map[string]string{}
	}
	if voice == "" {
		delete(v[name], tts)
	} else {
		v[name][tts] = voice
	}
	return writeJSON(s.voicesPath(), v)
}

// VoiceSamples — реплики персонажа для копирования голоса: самые длинные (но не длиннее 15 с — там
// бывает музыка), пока не наберётся seconds. Возвращает файлы WAV и сколько в них секунд речи.
func (s *Session) VoiceSamples(ctx context.Context, name string, seconds float64) ([]string, float64, error) {
	cache := s.loadPrints()
	type cand struct {
		i   int
		sec float64
	}
	var cands []cand
	for i, l := range s.Audio {
		if l.Speaker != name || l.Transcript == "" {
			continue
		}
		vp, ok := cache[l.File]
		if !ok {
			p, err := s.decodeAudio(ctx, l.File)
			if err != nil {
				continue
			}
			vp.Seconds = float64(len(speechPart(p))) / 16000
		}
		if vp.Seconds >= 1 {
			cands = append(cands, cand{i, vp.Seconds})
		}
	}
	score := func(sec float64) float64 {
		if sec > 15 {
			return 15 - (sec - 15) // длинное — подальше: там чаще музыка и шум
		}
		return sec
	}
	sort.SliceStable(cands, func(a, b int) bool { return score(cands[a].sec) > score(cands[b].sec) })
	var out []string
	total := 0.0
	for _, c := range cands {
		if total >= seconds || len(out) >= 20 {
			break
		}
		wav, err := s.AudioWav(ctx, s.Audio[c.i].File)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, wav)
		total += c.sec
	}
	return out, total, nil
}

// cloneName — имя скопированного голоса: голос персонажа и игра (голоса XTTS общие для всех игр).
func (s *Session) cloneName(speaker string) string {
	game := strings.TrimSuffix(filepath.Base(s.WorkDir), " — перевод")
	if i := strings.Index(game, " ["); i > 0 {
		game = game[:i]
	}
	return speaker + " (" + game + ")"
}

// minCloneSeconds — меньше этого голос не скопировать сносно: персонаж озвучивается голосом по умолчанию.
const minCloneSeconds = 5

// DubVoices озвучивает реплики idx голосами их персонажей. Персонажу без голоса озвучки голос копируется
// по его же репликам (если озвучка id это умеет). Реплики без голоса — голосом по умолчанию из cfg.
func (s *Session) DubVoices(ctx context.Context, id string, cfg speech.Config, idx []int, progress Progress) error {
	groups := map[string][]int{}
	var order []string
	for _, i := range idx {
		name := s.Audio[i].Speaker
		if groups[name] == nil {
			order = append(order, name)
		}
		groups[name] = append(groups[name], i)
	}
	sort.SliceStable(order, func(a, b int) bool {
		if (order[a] == "") != (order[b] == "") {
			return order[b] == ""
		}
		return len(groups[order[a]]) > len(groups[order[b]])
	})
	for _, name := range order {
		vcfg := cfg
		label := "Озвучка"
		if name != "" {
			label += " — " + name
			voice, err := s.speakerVoice(ctx, id, cfg, name, groups[name], progress)
			if err != nil {
				return err
			}
			if voice != "" {
				vcfg.Model = voice
			}
		}
		tts, err := speech.NewSynthesizer(id, vcfg)
		if err != nil {
			return err
		}
		if tts == nil {
			return fmt.Errorf("выбери, чем озвучивать")
		}
		if err := s.dubLines(ctx, tts, groups[name], label, progress); err != nil {
			return err
		}
	}
	return nil
}

// speakerVoice — голос озвучки персонажа; если его нет, а озвучка умеет копировать, — копирует.
func (s *Session) speakerVoice(ctx context.Context, id string, cfg speech.Config, name string, lines []int, progress Progress) (string, error) {
	voice := s.SpeakerVoice(name, id)
	if voice != "" && speech.VoiceUsable(id, voice) {
		return voice, nil
	}
	if !speech.CanClone(id) {
		return "", nil
	}
	todo := false
	for _, i := range lines {
		if !s.Audio[i].HandDub && s.Audio[i].Translation != "" {
			todo = true
		}
	}
	if !todo {
		return "", nil // озвучивать нечего — и голос копировать незачем
	}
	progress("Копирование голоса «"+name+"»", 0, 1)
	label, err := s.CloneSpeaker(ctx, id, cfg, name)
	if errors.Is(err, errFewSamples) {
		s.log("«%s»: %v — озвучу голосом по умолчанию", name, err)
		return "", nil
	}
	return label, err
}

var errFewSamples = errors.New("речи слишком мало, чтобы скопировать голос")

// CloneSpeaker копирует голос персонажа по его репликам и запоминает его для озвучки id.
func (s *Session) CloneSpeaker(ctx context.Context, id string, cfg speech.Config, name string) (string, error) {
	samples, sec, err := s.VoiceSamples(ctx, name, speech.CloneSeconds(id))
	if err != nil {
		return "", err
	}
	if sec < minCloneSeconds {
		return "", fmt.Errorf("%w: всего %.0f с", errFewSamples, sec)
	}
	label, err := speech.CloneVoice(ctx, id, cfg, s.cloneName(name), samples)
	if err != nil {
		return "", fmt.Errorf("голос «%s»: %w", name, err)
	}
	s.log("Голос «%s» скопирован по %d репликам (%.0f с речи): %s", name, len(samples), sec, label)
	return label, s.SetSpeakerVoice(name, id, label)
}
