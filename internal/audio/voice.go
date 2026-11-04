package audio

import "math"

// Определение голоса без Whisper: по тому, как меняется громкость. Речь идёт слогами — громкость
// «пульсирует» 2–8 раз в секунду и часто проваливается в паузы; голос в играх обычно моно.
// Музыка ровнее и чаще стерео, эффекты — короткие. Флаг «закольцован» не используется: в некоторых играх
// (Ring Fit) он стоит у всех звуков. Это подсказка, а не распознавание.

// VoiceFeatures — признаки звука.
type VoiceFeatures struct {
	Seconds   float64
	Stereo    bool
	Loop      bool
	Syllabic  float64 // доля «слоговых» колебаний громкости (2–8 Гц) среди всех медленных колебаний
	Pauses    float64 // доля тихих кадров среди звучащих
	ZCRSpread float64 // насколько меняется «шипучесть» от кадра к кадру (гласные ↔ согласные)
	Silent    bool
}

// Features считает признаки (кадры по 20 мс на моно 16 кГц).
func Features(p *PCM) VoiceFeatures {
	f := VoiceFeatures{Seconds: p.Seconds(), Stereo: len(p.Channels) > 1, Loop: p.Loop}
	s := Mono16k(p)
	const frame = 320
	n := len(s) / frame
	if n < 5 {
		f.Silent = true
		return f
	}
	env := make([]float64, n)
	zcr := make([]float64, n)
	peak := 0.0
	for i := range n {
		var e float64
		z := 0
		for j := i * frame; j < (i+1)*frame; j++ {
			e += float64(s[j]) * float64(s[j])
			if j > i*frame && (s[j] >= 0) != (s[j-1] >= 0) {
				z++
			}
		}
		env[i] = math.Sqrt(e / frame)
		zcr[i] = float64(z) / frame
		peak = math.Max(peak, env[i])
	}
	if peak < 1e-3 {
		f.Silent = true
		return f
	}
	// звучащие кадры — не тише пика на 40 дБ; паузы — тише среднего звучащего втрое
	var active []int
	mean := 0.0
	for i, e := range env {
		if e > peak*0.01 {
			active = append(active, i)
			mean += e
		}
	}
	if len(active) < 5 {
		f.Silent = true
		return f
	}
	mean /= float64(len(active))
	quiet := 0
	var zs []float64
	for _, i := range active {
		if env[i] < mean/3 {
			quiet++
		}
		zs = append(zs, zcr[i])
	}
	f.Pauses = float64(quiet) / float64(len(active))
	f.ZCRSpread = stddev(zs) / math.Max(meanOf(zs), 1e-6)

	// спектр огибающей громкости (50 кадров в секунду): сколько энергии в 2–8 Гц из 0.5–20 Гц
	m := meanOf(env)
	var syl, all float64
	for hz := 0.5; hz <= 20; hz += 0.25 {
		var re, im float64
		w := 2 * math.Pi * hz / 50
		for i, e := range env {
			re += (e - m) * math.Cos(w*float64(i))
			im += (e - m) * math.Sin(w*float64(i))
		}
		pw := re*re + im*im
		all += pw
		if hz >= 2 && hz <= 8 {
			syl += pw
		}
	}
	if all > 0 {
		f.Syllabic = syl / all
	}
	return f
}

// Voice — вердикт: «голос», «музыка», «эффект» или «тишина» и уверенность 0–1.
func (f VoiceFeatures) Voice() (kind string, score float64) {
	switch {
	case f.Silent:
		return "тишина", 1
	case f.Seconds < 0.3:
		return "эффект", 0.8
	}
	score = 0
	score += 1.5 * clamp01((f.Syllabic-0.25)/0.35)
	score += 1.0 * clamp01((f.Pauses-0.1)/0.3)
	score += 0.8 * clamp01((f.ZCRSpread-0.3)/0.6)
	if f.Stereo {
		score -= 0.4 // голос в играх почти всегда моно, музыка — стерео
	} else {
		score += 0.5
	}
	if f.Seconds > 60 {
		score -= 1
	}
	score /= 3.8
	switch {
	case score >= 0.5:
		return "голос", score
	case f.Seconds > 20 || f.Stereo:
		return "музыка", 1 - score
	}
	return "эффект", 1 - score
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stddev(v []float64) float64 {
	m := meanOf(v)
	s := 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / math.Max(float64(len(v)), 1))
}
