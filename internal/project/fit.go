package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"switch-translator/internal/audio"
	"switch-translator/internal/ffmpeg"
)

// Подгонка озвучки по длине: русская фраза обычно длиннее английской, а игра рассчитана на длину
// оригинала — длинную реплику она может оборвать или наложить на следующую. Перед вписыванием
// у озвучки срезается тишина по краям, и если она всё ещё длиннее оригинала, её ускоряют
// без изменения высоты голоса (ffmpeg atempo), но не больше чем в MaxSpeed раз — иначе речь
// становится неразборчивой. Файлы озвучки при этом не меняются: подгонка делается при сборке.

// MaxSpeed — во сколько раз можно ускорить речь, чтобы её ещё было легко понять.
const MaxSpeed = 1.5

// Fit — как озвучка легла на оригинал.
type Fit struct {
	Orig  float64 // длина оригинала, с
	Dub   float64 // длина озвучки как она записана, с
	Out   float64 // длина после подгонки, с
	Speed float64 // во сколько раз ускорена (1 — не ускорялась)
}

// TooLong — озвучка и после ускорения заметно длиннее оригинала: перевод стоит сократить.
func (f Fit) TooLong() bool { return f.Orig > 0 && f.Out > f.Orig*1.1+0.2 }

// Describe — по-русски, для подсказки в правке звука.
func (f Fit) Describe() string {
	s := fmt.Sprintf("в игре %.1f с при оригинале %.1f с", f.Out, f.Orig)
	if f.Speed > 1.01 {
		s += fmt.Sprintf(", ускорено в %.2g раза", f.Speed)
	}
	if f.TooLong() {
		s += " — всё равно длиннее: сократи перевод"
	}
	return s
}

// fitDub подгоняет озвучку p под длину оригинала orig (в секундах).
func fitDub(ctx context.Context, p *audio.PCM, orig float64, tmp string) (*audio.PCM, Fit, error) {
	f := Fit{Orig: orig, Dub: p.Seconds(), Speed: 1}
	p = trimSilence(p)
	if orig <= 0 || p.Seconds() <= orig*1.02 || !ffmpeg.Available() {
		f.Out = p.Seconds()
		return p, f, nil
	}
	speed := min(p.Seconds()/orig, MaxSpeed)
	out, err := atempo(ctx, p, speed, tmp)
	if err != nil {
		return nil, f, err
	}
	f.Out, f.Speed = out.Seconds(), speed
	return out, f, nil
}

// trimSilence срезает тишину в начале и в конце, оставляя по 40 мс.
func trimSilence(p *audio.PCM) *audio.PCM {
	n := p.Samples()
	if n == 0 || p.Rate == 0 {
		return p
	}
	const quiet = 330 // ≈ −40 дБ
	loud := func(i int) bool {
		for _, ch := range p.Channels {
			if ch[i] > quiet || ch[i] < -quiet {
				return true
			}
		}
		return false
	}
	a, b := 0, n
	for a < n && !loud(a) {
		a++
	}
	for b > a && !loud(b-1) {
		b--
	}
	if a >= b {
		return p // одна тишина — оставляем как есть
	}
	pad := p.Rate / 25
	a, b = max(0, a-pad), min(n, b+pad)
	if a == 0 && b == n {
		return p
	}
	out := &audio.PCM{Rate: p.Rate}
	for _, ch := range p.Channels {
		out.Channels = append(out.Channels, ch[a:b])
	}
	return out
}

// atempo ускоряет речь в speed раз без изменения высоты голоса.
func atempo(ctx context.Context, p *audio.PCM, speed float64, tmp string) (*audio.PCM, error) {
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	in, err := os.CreateTemp(tmp, "fit-*.wav")
	if err != nil {
		return nil, err
	}
	inPath, outPath := in.Name(), in.Name()+".out.wav"
	defer os.Remove(inPath)
	defer os.Remove(outPath)
	_, err = in.Write(audio.WAV(p))
	if cerr := in.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	cmd, err := ffmpeg.Command("-loglevel", "error", "-y", "-i", inPath,
		"-af", "atempo="+strconv.FormatFloat(speed, 'f', 4, 64), "-c:a", "pcm_s16le", outPath)
	if err != nil {
		return nil, err
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ускорение озвучки: %v: %s", err, b)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	return audio.ReadWAV(b)
}

// FitDub — как озвучка файла i ляжет на оригинал (для правки звука и сборки).
func (s *Session) FitDub(ctx context.Context, i int) (*audio.PCM, Fit, error) {
	l := s.Audio[i]
	b, err := os.ReadFile(l.Dub)
	if err != nil {
		return nil, Fit{}, err
	}
	p, err := audio.ReadWAV(b)
	if err != nil {
		return nil, Fit{}, fmt.Errorf("озвучка не в WAV — озвучь заново: %w", err)
	}
	orig, err := s.decodeAudio(ctx, l.File)
	if err != nil {
		return nil, Fit{}, err
	}
	return fitDub(ctx, p, orig.Seconds(), filepath.Join(s.WorkDir, ".tmp"))
}

// FittedDubWav — подогнанная озвучка файла i во временном WAV: послушать, как будет в игре.
func (s *Session) FittedDubWav(ctx context.Context, i int) (string, Fit, error) {
	p, f, err := s.FitDub(ctx, i)
	if err != nil {
		return "", f, err
	}
	out := filepath.Join(s.WorkDir, ".tmp", "в-игре-"+s.audioBase(s.Audio[i].File)+".wav")
	return out, f, writeFile(out, audio.WAV(p))
}
