package speech

import (
	"errors"
	"math/rand"
	"testing"
)

// Три «голоса» — три направления в пространстве отпечатков, реплики — с шумом вокруг них.
func voicePrints(t *testing.T, per []int, noise float32) (prints [][]float32, weight []float64, truth []int) {
	t.Helper()
	r := rand.New(rand.NewSource(1))
	const dim = 64
	for v, count := range per {
		center := make([]float32, dim)
		for d := range center {
			center[d] = float32(r.NormFloat64())
		}
		normalize(center)
		for range count {
			p := make([]float32, dim)
			for d := range p {
				p[d] = center[d] + noise*float32(r.NormFloat64())/8
			}
			prints = append(prints, normalize(p))
			weight = append(weight, 1+r.Float64()*3)
			truth = append(truth, v)
		}
	}
	return
}

// purity — в каждой найденной группе один настоящий голос, и голосов столько же.
func checkGroups(t *testing.T, got, truth []int, want int) {
	t.Helper()
	owner := map[int]int{}
	groups := map[int]bool{}
	for i, g := range got {
		if g < 0 {
			t.Fatalf("реплика %d без голоса", i)
		}
		groups[g] = true
		if o, ok := owner[g]; ok && o != truth[i] {
			t.Fatalf("в группе %d разные голоса", g)
		}
		owner[g] = truth[i]
	}
	if len(groups) != want {
		t.Fatalf("групп %d, а голосов %d", len(groups), want)
	}
}

func TestClusterVoices(t *testing.T) {
	prints, weight, truth := voicePrints(t, []int{120, 60, 15}, 0.7)
	got := ClusterVoices(prints, weight, 0, VoiceThreshold)
	checkGroups(t, got, truth, 3)
	// самый частый голос — первый
	if got[0] != 0 || got[len(got)-1] != 2 {
		t.Fatalf("номера не по числу реплик: %d, %d", got[0], got[len(got)-1])
	}
	// заданное число голосов
	checkGroups(t, ClusterVoices(prints, weight, 3, VoiceThreshold), truth, 3)
	if g := ClusterVoices(prints, weight, 2, VoiceThreshold); g[0] == g[len(g)-1] && g[0] == g[130] {
		t.Fatal("при двух группах всё слилось в одну")
	}
}

// Реплик больше, чем группируется попарно: остальные примыкают к ближайшей группе.
func TestClusterVoicesMany(t *testing.T) {
	old := maxClusterAnchors
	defer func() { maxClusterAnchors = old }()
	maxClusterAnchors = 50
	prints, weight, truth := voicePrints(t, []int{100, 80}, 0.7)
	checkGroups(t, ClusterVoices(prints, weight, 0, VoiceThreshold), truth, 2)
}

// Одиночные непохожие реплики (вскрики, шум) не становятся отдельными голосами.
func TestClusterVoicesStrays(t *testing.T) {
	prints, weight, truth := voicePrints(t, []int{80, 40}, 0.7)
	r := rand.New(rand.NewSource(7))
	for range 5 {
		p := make([]float32, len(prints[0]))
		for d := range p {
			p[d] = float32(r.NormFloat64())
		}
		prints = append(prints, normalize(p))
		weight = append(weight, 5) // длинные — попадают в опорные
		truth = append(truth, -1)
	}
	got := ClusterVoices(prints, weight, 0, VoiceThreshold)
	groups := map[int]bool{}
	for i, g := range got {
		if g >= 0 {
			groups[g] = true
		}
		if truth[i] >= 0 && g < 0 {
			t.Fatalf("реплика %d голоса %d осталась без голоса", i, truth[i])
		}
	}
	if len(groups) != 2 {
		t.Fatalf("голосов %d, а должно быть 2: %v", len(groups), got[len(got)-5:])
	}
}

func TestMissingModule(t *testing.T) {
	err := errors.New("XTTS: модель не загрузилась: No module named 'six'")
	if m := missingModule(err); m != "six" {
		t.Fatalf("модуль %q", m)
	}
	if m := missingModule(errors.New("No module named 'yaml.cyaml'")); pipName(m) != "pyyaml" {
		t.Fatalf("пакет для %q — %q", m, pipName(m))
	}
	if missingModule(errors.New("CUDA out of memory")) != "" {
		t.Fatal("не та ошибка")
	}
}
