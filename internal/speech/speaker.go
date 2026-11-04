package speech

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"sort"
)

// Различение голосов: у каждой реплики считается «отпечаток голоса» — вектор, который у одного
// человека похож от фразы к фразе, а у разных людей разный (модель CAM++ из 3D-Speaker, обучена
// на VoxCeleb, через sherpa-onnx в окружении Python — без torch, около 30 МБ). Затем реплики с
// похожими отпечатками собираются в группы — это и есть голоса персонажей.

const speakerModelName = "3dspeaker_speech_campplus_sv_en_voxceleb_16k.onnx"

func speakerModel() string { return filepath.Join(ToolsDir(), "speakers", speakerModelName) }

// SpeakersReady — можно ли различать голоса и что для этого поставить.
func SpeakersReady() (bool, string) {
	if !pythonReady("speakers") || !fileExists(speakerModel()) {
		return false, "Нужен Python (3.10–3.13 с python.org); программа поставит в своё окружение sherpa-onnx и скачает модель отпечатка голоса — всего около 40 МБ."
	}
	return true, "модель CAM++ на этом компьютере"
}

// SetupSpeakers ставит то, что нужно для различения голосов.
func SetupSpeakers(ctx context.Context, log func(string), progress func(done, total int64)) error {
	if !pythonHas("sherpa_onnx", "numpy") {
		if err := installPython(ctx, []string{"sherpa-onnx", "numpy"}, log); err != nil {
			return err
		}
	}
	if !fileExists(speakerModel()) {
		log("Скачиваю модель отпечатка голоса (30 МБ)")
		url := "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/" + speakerModelName
		if err := downloadFile(ctx, url, speakerModel(), progress); err != nil {
			return fmt.Errorf("не удалось скачать модель отпечатка голоса: %w", err)
		}
	}
	if err := pyPing(ctx, "speakers", speakerScript, speakerEnv(), log); err != nil {
		return err
	}
	markPython("speakers")
	return nil
}

func speakerEnv() []string { return []string{"SWTR_SPK_MODEL=" + speakerModel()} }

const speakerScript = `
import os, sys, json, base64
M = "@@SWTR "
def reply(d):
    sys.stdout.write(M + json.dumps(d) + "\n"); sys.stdout.flush()
try:
    import numpy as np
    import sherpa_onnx
    cfg = sherpa_onnx.SpeakerEmbeddingExtractorConfig(model=os.environ["SWTR_SPK_MODEL"], num_threads=2)
    if not cfg.validate():
        raise ValueError("модель не подходит")
    ext = sherpa_onnx.SpeakerEmbeddingExtractor(cfg)
    err = None
except BaseException as e:
    err = "Различение голосов: модель не загрузилась: %s" % e
for line in sys.stdin:
    if err:
        reply({"error": err}); continue
    try:
        r = json.loads(line)
        if r.get("op") == "ping":
            reply({"ok": True, "dim": ext.dim}); continue
        x = np.frombuffer(base64.b64decode(r["pcm"]), dtype="<i2").astype(np.float32) / 32768.0
        s = ext.create_stream()
        s.accept_waveform(sample_rate=16000, waveform=x)
        s.input_finished()
        if not ext.is_ready(s):
            reply({"error": "слишком короткий звук"}); continue
        reply({"emb": [float(v) for v in ext.compute(s)]})
    except Exception as e:
        reply({"error": "Различение голосов: %s" % e})
`

// VoicePrint — отпечаток голоса речи (моно 16 кГц, −1…1), длиной 1.
func VoicePrint(ctx context.Context, mono16k []float32) ([]float32, error) {
	if !pythonReady("speakers") || !fileExists(speakerModel()) {
		return nil, fmt.Errorf("различение голосов не установлено")
	}
	pcm := make([]byte, 2*len(mono16k))
	for i, v := range mono16k {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(max(-1, min(1, v))*32767)))
	}
	m, err := pyCall(ctx, "speakers", speakerScript, speakerEnv(), map[string]string{"pcm": base64.StdEncoding.EncodeToString(pcm)})
	if err != nil {
		return nil, err
	}
	raw, _ := m["emb"].([]any)
	if len(raw) == 0 {
		return nil, fmt.Errorf("различение голосов: пустой ответ")
	}
	v := make([]float32, len(raw))
	for i, x := range raw {
		f, _ := x.(float64)
		v[i] = float32(f)
	}
	return normalize(v), nil
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	k := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= k
	}
	return v
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range min(len(a), len(b)) {
		s += a[i] * b[i]
	}
	return s
}

// VoiceThreshold — с какой похожести (косинус отпечатков, в среднем между группами) реплики считаются одним голосом.
const VoiceThreshold = 0.5

// minGroupLines — сколько реплик нужно группе, чтобы считаться голосом (если число голосов не задано).
const minGroupLines = 3

// maxClusterAnchors — сколько реплик группируется попарно; остальные (обычно короткие) примыкают к ближайшей группе.
var maxClusterAnchors = 3000

// ClusterVoices раскладывает отпечатки по голосам: номер группы для каждого (−1 — голос не определить).
// Группы нумеруются по убыванию веса (weight — длина реплики: лучше узнаётся и важнее для озвучки).
// want > 0 — ровно столько голосов (если реплик хватает); иначе группы сливаются, пока средняя похожесть
// не ниже threshold. Отпечатки должны быть длиной 1.
func ClusterVoices(prints [][]float32, weight []float64, want int, threshold float64) []int {
	n := len(prints)
	out := make([]int, n)
	for i := range out {
		out[i] = -1
	}
	if n == 0 {
		return out
	}
	// опорные — самые длинные реплики: по ним группы надёжнее
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return weight[order[a]] > weight[order[b]] })
	anchors := order[:min(n, maxClusterAnchors)]
	labels := averageLinkage(prints, anchors, want, threshold)
	k := 0
	for _, l := range labels {
		k = max(k, l+1)
	}
	// группа из одной-двух реплик — почти всегда не персонаж, а вскрик, шум или ошибка распознавания
	// (в Ring Fit их выходило два десятка); такие реплики примыкают к ближайшему голосу, как короткие
	count := make([]int, k)
	for _, l := range labels {
		count[l]++
	}
	big := 0
	for _, c := range count {
		if c >= minGroupLines {
			big++
		}
	}
	rest := order[len(anchors):]
	if big > 0 && want <= 0 {
		var keep []int
		var keepLabels []int
		for j, a := range anchors {
			if count[labels[j]] >= minGroupLines {
				keep = append(keep, a)
				keepLabels = append(keepLabels, labels[j])
			} else {
				rest = append(rest, a)
			}
		}
		anchors, labels = keep, keepLabels
	}

	// центр и вес каждой группы
	dim := len(prints[anchors[0]])
	centers := make([][]float32, k)
	mass := make([]float64, k)
	for i := range centers {
		centers[i] = make([]float32, dim)
	}
	for j, a := range anchors {
		l := labels[j]
		for d, x := range prints[a] {
			centers[l][d] += x
		}
		mass[l] += weight[a]
	}
	for i := range centers {
		normalize(centers[i])
	}
	// остальные — к ближайшей группе, если похожи хоть немного
	for _, i := range rest {
		best, bestSim := -1, float32(threshold)*0.6
		for l, c := range centers {
			if mass[l] == 0 {
				continue // распущенная маленькая группа
			}
			if s := dot(prints[i], c); s > bestSim {
				best, bestSim = l, s
			}
		}
		if best >= 0 {
			out[i] = best
			mass[best] += weight[i]
		}
	}
	for j, a := range anchors {
		out[a] = labels[j]
	}
	// номера по убыванию веса
	rank := make([]int, k)
	for i := range rank {
		rank[i] = i
	}
	sort.SliceStable(rank, func(a, b int) bool { return mass[rank[a]] > mass[rank[b]] })
	newID := make([]int, k)
	for pos, l := range rank {
		newID[l] = pos
	}
	for i, l := range out {
		if l >= 0 {
			out[i] = newID[l]
		}
	}
	return out
}

// averageLinkage — иерархическая группировка со средней связью (UPGMA) цепочкой ближайших соседей:
// O(n²) времени и памяти. Возвращает номер группы для каждой из idx (0…k−1).
func averageLinkage(prints [][]float32, idx []int, want int, threshold float64) []int {
	n := len(idx)
	if n == 1 {
		return []int{0}
	}
	sim := make([]float32, n*n)
	for i := range n {
		for j := i + 1; j < n; j++ {
			s := dot(prints[idx[i]], prints[idx[j]])
			sim[i*n+j], sim[j*n+i] = s, s
		}
	}
	size := make([]float64, n)
	alive := make([]bool, n)
	for i := range n {
		size[i], alive[i] = 1, true
	}
	type merge struct {
		a, b int
		sim  float32
	}
	merges := make([]merge, 0, n-1)
	var chain []int
	left := n
	for left > 1 {
		if len(chain) == 0 {
			for i := range n {
				if alive[i] {
					chain = append(chain, i)
					break
				}
			}
		}
		a := chain[len(chain)-1]
		prev := -1
		if len(chain) >= 2 {
			prev = chain[len(chain)-2]
		}
		b, bs := prev, float32(math.Inf(-1))
		if prev >= 0 {
			bs = sim[a*n+prev]
		}
		for c := range n {
			if c != a && alive[c] && sim[a*n+c] > bs {
				b, bs = c, sim[a*n+c]
			}
		}
		if b != prev {
			chain = append(chain, b)
			continue
		}
		// a и prev — взаимно ближайшие: сливаем в a
		chain = chain[:len(chain)-2]
		merges = append(merges, merge{a, prev, bs})
		na, nb := size[a], size[prev]
		for c := range n {
			if alive[c] && c != a && c != prev {
				s := float32((na*float64(sim[a*n+c]) + nb*float64(sim[prev*n+c])) / (na + nb))
				sim[a*n+c], sim[c*n+a] = s, s
			}
		}
		size[a] += nb
		alive[prev] = false
		left--
	}
	// слияния по убыванию похожести; средняя связь монотонна, так что это настоящее дерево групп
	sort.SliceStable(merges, func(i, j int) bool { return merges[i].sim > merges[j].sim })
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	groups := n
	for _, m := range merges {
		if want > 0 && groups <= want || want <= 0 && float64(m.sim) < threshold {
			break
		}
		parent[find(m.b)] = find(m.a)
		groups--
	}
	ids := map[int]int{}
	out := make([]int, n)
	for i := range n {
		r := find(i)
		id, ok := ids[r]
		if !ok {
			id = len(ids)
			ids[r] = id
		}
		out[i] = id
	}
	return out
}
