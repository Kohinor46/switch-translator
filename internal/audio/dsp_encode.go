package audio

import "math"

// Кодирование в DSP-ADPCM — сжатие, которым игры Nintendo хранят речь: кадр 8 байт на 14 сэмплов,
// в 3,5 раза меньше PCM. Звук игры часто лежит в памяти целиком, и несжатая озвучка туда может
// не поместиться — поэтому новая озвучка пишется тем же сжатием, что оригинал.

// dspDefaultCoefs — 8 пар предсказателей на случай, когда взять их у оригинала нельзя:
// без предсказания, первый порядок и несколько вторых — от плавного звука до резкого.
var dspDefaultCoefs = [16]int16{
	0, 0, 2048, 0, 0, 2048, 1024, 1024,
	4096, -2048, 3584, -1536, 3072, -1024, 4608, -2560,
}

// dspFrameBytes — сколько байт займут samples сэмплов.
func dspFrameBytes(samples int) int { return (samples + 13) / 14 * 8 }

// encodeDSP сжимает канал. Для каждого кадра перебираются все предсказатели и масштабы
// и берётся тот, что ближе всего к исходнику после расшифровки (история — как у декодера).
func encodeDSP(src []int16, coefs [16]int16) []byte {
	out := make([]byte, dspFrameBytes(len(src)))
	var h1, h2 int32
	var frame [14]int32
	var best, try [14]int32
	for f := 0; f*14 < len(src); f++ {
		n := min(14, len(src)-f*14)
		for i := range 14 {
			frame[i] = 0
			if i < n {
				frame[i] = int32(src[f*14+i])
			}
		}
		bestErr := math.Inf(1)
		var bestPS byte
		var bestH1, bestH2 int32
		for p := range 8 {
			c1, c2 := int32(coefs[p*2]), int32(coefs[p*2+1])
			for scale := range 13 {
				e, t1, t2 := dspTryFrame(frame[:n], c1, c2, scale, h1, h2, &try)
				if e < bestErr {
					bestErr, bestPS, bestH1, bestH2, best = e, byte(p<<4|scale), t1, t2, try
				}
			}
		}
		o := out[f*8:]
		o[0] = bestPS
		for i := range n {
			nib := byte(best[i] & 0xF)
			if i%2 == 0 {
				o[1+i/2] |= nib << 4
			} else {
				o[1+i/2] |= nib
			}
		}
		h1, h2 = bestH1, bestH2
	}
	return out
}

// dspTryFrame кодирует кадр одним предсказателем и масштабом: ошибка, история после кадра, полубайты.
func dspTryFrame(frame []int32, c1, c2 int32, scale int, h1, h2 int32, nibs *[14]int32) (float64, int32, int32) {
	step := int64(1) << scale
	var e float64
	for i, x := range frame {
		pred := int64(c1)*int64(h1) + int64(c2)*int64(h2) + 1024
		// nib*step<<11 + pred ≈ x<<11 → nib ≈ (x<<11 − pred) / (step<<11)
		d := float64(int64(x)<<11-pred) / float64(step<<11)
		nib := int32(math.Round(d))
		nib = max(-8, min(7, nib))
		s := (int64(nib)*step<<11 + pred) >> 11
		s = max(math.MinInt16, min(math.MaxInt16, s))
		diff := float64(int64(x) - s)
		e += diff * diff
		nibs[i] = nib
		h2, h1 = h1, int32(s)
	}
	return e, h1, h2
}
