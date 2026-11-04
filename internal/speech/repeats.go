package speech

import (
	"strings"
	"unicode"

	"switch-translator/internal/whisper"
)

// CleanTranscript убирает «заедание» распознавания: Whisper на короткой реплике иногда повторяет
// одно и то же до конца окна — «Flutter kick Flutter kick Flutter kick…». Если фраза из 1–12 слов
// повторяется подряд 3 раза и больше (одно слово — 5 раз), остаётся один раз, а хвост после
// заедания отбрасывается: дальше Whisper уже не слушает звук. changed — была ли правка.
func CleanTranscript(text string) (clean string, changed bool) {
	words := strings.Fields(text)
	norm := make([]string, len(words))
	for i, w := range words {
		norm[i] = strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
	}
	for i := range words {
		for n := 1; n <= 12 && i+n <= len(words); n++ {
			need := 3
			if n == 1 {
				need = 5
			}
			reps := 1
			for j := i + n; j+n <= len(words) && same(norm[i:i+n], norm[j:j+n]); j += n {
				reps++
			}
			if reps >= need {
				return strings.Join(words[:i+n], " "), true
			}
		}
	}
	return text, false
}

func same(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] || a[i] == "" {
			return false
		}
	}
	return true
}

// CollapseDouble — фраза, записанная дважды подряд («Good effort. Good effort.»), остаётся один раз,
// если для двух повторов звук слишком короткий: Whisper на коротких репликах иногда удваивает текст.
// Настоящий повтор («Ещё! Ещё!») укладывается в обычный темп речи и остаётся. seconds — длина звука.
func CollapseDouble(text string, seconds float64) (string, bool) {
	words := strings.Fields(text)
	if seconds <= 0 || len(words) < 2 || len(words)%2 != 0 {
		return text, false
	}
	half := len(words) / 2
	norm := func(w string) string {
		return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
	}
	syl := 0
	for i := range half {
		a, b := norm(words[i]), norm(words[half+i])
		if a == "" || a != b {
			return text, false
		}
		n := syllables(a)
		if n == 0 {
			return text, false // не алфавит с гласными (японский, китайский) — не берёмся судить
		}
		syl += n
	}
	// обычная речь — 4–5 слогов в секунду, быстрая — около 6; удвоенный текст быстрее этого не произнести
	if float64(2*syl)/seconds <= 5.5 {
		return text, false
	}
	return strings.Join(words[:half], " "), true
}

// syllables — примерное число слогов: группы гласных подряд.
func syllables(w string) int {
	n, prev := 0, false
	for _, r := range w {
		v := strings.ContainsRune("aeiouyаеёиоуыэюя", unicode.ToLower(r))
		if v && !prev {
			n++
		}
		prev = v
	}
	return n
}

// ShortenStretched укорачивает протяжную букву («AHHHH…» на сотни знаков) до пяти.
func ShortenStretched(text string) string { return whisper.ShortenStretched(text) }
