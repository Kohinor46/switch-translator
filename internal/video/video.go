// Package video — ролики игры через вшитый ffmpeg: параметры, звук для распознавания
// и пересборка с субтитрами и озвучкой. Ролик кодируется тем же кодеком, в том же разрешении,
// с той же частотой кадров и примерно тем же битрейтом — иначе игра может его не проиграть.
package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"switch-translator/internal/ffmpeg"
)

// Info — что внутри ролика.
type Info struct {
	Container string  `json:"container"`
	Duration  float64 `json:"duration"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       string  `json:"fps"`
	VCodec    string  `json:"vcodec"`
	Profile   string  `json:"profile,omitempty"`
	Level     int     `json:"level,omitempty"`
	PixFmt    string  `json:"pix_fmt,omitempty"`
	VBitrate  int64   `json:"vbitrate,omitempty"`
	ACodec    string  `json:"acodec,omitempty"`
	ARate     int     `json:"arate,omitempty"`
	AChannels int     `json:"achannels,omitempty"`
	ABitrate  int64   `json:"abitrate,omitempty"`
	Audio     int     `json:"audio_tracks,omitempty"`
}

// Describe — коротко по-русски: «VP9 1920×1080, 30 к/с, 1:25, звук Opus».
func (i *Info) Describe() string {
	s := fmt.Sprintf("%s %d×%d", strings.ToUpper(i.VCodec), i.Width, i.Height)
	if f := i.fps(); f > 0 {
		s += fmt.Sprintf(", %.4g к/с", f)
	}
	d := int(math.Round(i.Duration))
	s += fmt.Sprintf(", %d:%02d", d/60, d%60)
	if i.Audio == 0 {
		return s + ", без звука"
	}
	return s + ", звук " + i.ACodec
}

func (i *Info) fps() float64 {
	a, b, ok := strings.Cut(i.FPS, "/")
	n, _ := strconv.ParseFloat(a, 64)
	if !ok {
		return n
	}
	d, _ := strconv.ParseFloat(b, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

// Probe читает параметры ролика через ffprobe.
func Probe(ctx context.Context, path string) (*Info, error) {
	p, err := ffmpeg.ProbePath()
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, p, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", exitText(err))
	}
	var r struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
			Size     string `json:"size"`
		} `json:"format"`
		Streams []struct {
			Type       string `json:"codec_type"`
			Codec      string `json:"codec_name"`
			Profile    string `json:"profile"`
			Level      int    `json:"level"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			PixFmt     string `json:"pix_fmt"`
			RFrameRate string `json:"r_frame_rate"`
			AvgFrame   string `json:"avg_frame_rate"`
			BitRate    string `json:"bit_rate"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			Duration   string `json:"duration"`
			Tags       struct {
				BPS string `json:"BPS"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	i := &Info{Container: r.Format.Name, Duration: num(r.Format.Duration)}
	video := false
	for _, s := range r.Streams {
		br := int64(num(s.BitRate))
		if br == 0 {
			br = int64(num(s.Tags.BPS))
		}
		switch s.Type {
		case "video":
			if video {
				continue
			}
			video = true
			i.VCodec, i.Profile, i.Level, i.PixFmt = s.Codec, s.Profile, s.Level, s.PixFmt
			i.Width, i.Height, i.VBitrate = s.Width, s.Height, br
			i.FPS = s.AvgFrame
			if i.FPS == "" || i.FPS == "0/0" {
				i.FPS = s.RFrameRate
			}
			if i.Duration == 0 {
				i.Duration = num(s.Duration)
			}
		case "audio":
			if i.Audio == 0 {
				i.ACodec, i.ARate, i.AChannels, i.ABitrate = s.Codec, int(num(s.SampleRate)), s.Channels, br
			}
			i.Audio++
		}
	}
	if !video {
		return nil, fmt.Errorf("в файле нет видео")
	}
	if i.VBitrate == 0 && i.Duration > 0 {
		// у WebM битрейт потока обычно не записан — считаем по размеру файла
		total := int64(num(r.Format.BitRate))
		if total == 0 {
			total = int64(num(r.Format.Size) * 8 / i.Duration)
		}
		i.VBitrate = max(total-i.ABitrate*int64(i.Audio), total*8/10)
	}
	return i, nil
}

// ExtractAudio — звук ролика для распознавания: моно 16 кГц WAV.
func ExtractAudio(ctx context.Context, in, out string) error {
	return run(ctx, "", nil, 0, "-y", "-i", in, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", out)
}

// Sub — строка субтитров.
type Sub struct {
	Start, End float64
	Text       string
}

// Dub — озвучка куска: WAV и где он звучит.
type Dub struct {
	Start, End float64 // End — до какого времени можно звучать (начало следующей фразы или конец ролика)
	Wav        string
}

// Options — что сделать с роликом.
type Options struct {
	Subs     []Sub
	Font     []byte // TTF для субтитров
	FontName string // название семейства в TTF
	Dubs     []Dub
	Duck     float64 // громкость оригинала, пока звучит озвучка (0.25 — на четверть)
}

// Render пересобирает ролик: субтитры вшиваются в картинку, озвучка подмешивается в звук.
// progress получает долю готового (0–1).
func Render(ctx context.Context, in, out string, info *Info, o Options, progress func(float64)) error {
	work, err := os.MkdirTemp(filepath.Dir(out), "render-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// ffmpeg запускается в рабочей папке — входы и выход нужны абсолютными путями
	absIn, err := filepath.Abs(in)
	if err != nil {
		return err
	}
	args := []string{"-y", "-i", absIn}
	for _, d := range o.Dubs {
		w, err := filepath.Abs(d.Wav)
		if err != nil {
			return err
		}
		args = append(args, "-i", w)
	}
	var filters []string
	vOut := "0:v:0"
	if len(o.Subs) > 0 {
		if err := os.MkdirAll(filepath.Join(work, "fonts"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, "fonts", "subs.ttf"), o.Font, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, "subs.ass"), []byte(ASS(o.Subs, info.Width, info.Height, o.FontName)), 0o644); err != nil {
			return err
		}
		// пути относительные: ffmpeg запускается в рабочей папке, и в фильтре не нужно экранировать путь
		filters = append(filters, "[0:v:0]subtitles=subs.ass:fontsdir=fonts[v]")
		vOut = "[v]"
	}
	aOut := ""
	if len(o.Dubs) > 0 {
		f, label, err := dubFilter(ctx, info, o)
		if err != nil {
			return err
		}
		filters = append(filters, f)
		aOut = label
	}
	if len(filters) > 0 {
		args = append(args, "-filter_complex", strings.Join(filters, ";"))
	}
	args = append(args, "-map", vOut)
	if aOut != "" {
		args = append(args, "-map", aOut)
	} else if info.Audio > 0 {
		args = append(args, "-map", "0:a", "-c:a", "copy")
	}
	if len(o.Subs) > 0 {
		v, err := videoCodec(info)
		if err != nil {
			return err
		}
		args = append(args, v...)
	} else {
		args = append(args, "-c:v", "copy")
	}
	if aOut != "" {
		a, err := audioCodec(info)
		if err != nil {
			return err
		}
		args = append(args, a...)
	}
	args = append(args, "-fps_mode", "passthrough", "-map_metadata", "0")
	switch filepath.Ext(out) {
	case ".mp4", ".mov", ".m4v":
		args = append(args, "-movflags", "+faststart")
	case ".webm":
		args = append(args, "-f", "webm")
	}
	tmpOut := filepath.Join(work, "out"+filepath.Ext(out))
	abs, err := filepath.Abs(tmpOut)
	if err != nil {
		return err
	}
	args = append(args, abs)
	if err := run(ctx, work, progress, info.Duration, args...); err != nil {
		return err
	}
	return os.Rename(tmpOut, out)
}

// videoCodec — тот же кодек, профиль и примерно тот же битрейт, что у оригинала.
func videoCodec(i *Info) ([]string, error) {
	br := i.VBitrate
	if br <= 0 {
		br = 4_000_000
	}
	rate := func(k float64) string { return strconv.FormatInt(int64(float64(br)*k), 10) }
	pix := i.PixFmt
	if pix == "" {
		pix = "yuv420p"
	}
	switch i.VCodec {
	case "h264":
		a := []string{"-c:v", "libx264", "-preset", "medium", "-crf", "18", "-maxrate", rate(1.2), "-bufsize", rate(2), "-pix_fmt", pix}
		switch strings.ToLower(i.Profile) {
		case "baseline", "constrained baseline":
			a = append(a, "-profile:v", "baseline")
		case "main":
			a = append(a, "-profile:v", "main")
		case "high":
			a = append(a, "-profile:v", "high")
		}
		if i.Level > 0 {
			a = append(a, "-level:v", fmt.Sprintf("%d.%d", i.Level/10, i.Level%10))
		}
		return a, nil
	case "hevc":
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", "20", "-x265-params", "vbv-maxrate=" + rate(0.0012) + ":vbv-bufsize=" + rate(0.002), "-pix_fmt", pix, "-tag:v", "hvc1"}, nil
	case "vp9":
		return []string{"-c:v", "libvpx-vp9", "-b:v", rate(1), "-maxrate", rate(1.3), "-minrate", rate(0.5), "-crf", "28",
			"-deadline", "good", "-cpu-used", "2", "-row-mt", "1", "-pix_fmt", pix}, nil
	case "vp8":
		return []string{"-c:v", "libvpx", "-b:v", rate(1), "-maxrate", rate(1.3), "-crf", "8", "-deadline", "good", "-cpu-used", "2", "-pix_fmt", pix}, nil
	}
	return nil, fmt.Errorf("видео %s пересобрать нечем", i.VCodec)
}

func audioCodec(i *Info) ([]string, error) {
	br := i.ABitrate
	if br <= 0 {
		br = int64(64_000 * max(i.AChannels, 1))
	}
	rate := []string{}
	if i.ARate > 0 {
		rate = []string{"-ar", strconv.Itoa(i.ARate)}
	}
	b := strconv.FormatInt(br, 10)
	switch i.ACodec {
	case "opus":
		return append([]string{"-c:a", "libopus", "-b:a", b}, rate...), nil
	case "vorbis":
		if i.ABitrate <= 0 {
			// у WebM битрейт звука обычно не записан, а 128 кбит/с для моно 22 кГц libvorbis не принимает
			return append([]string{"-c:a", "libvorbis", "-q:a", "6"}, rate...), nil
		}
		return append([]string{"-c:a", "libvorbis", "-b:a", b}, rate...), nil
	case "aac":
		return append([]string{"-c:a", "aac", "-b:a", b}, rate...), nil
	case "mp3":
		return append([]string{"-c:a", "libmp3lame", "-b:a", b}, rate...), nil
	case "pcm_s16le", "":
		return append([]string{"-c:a", "pcm_s16le"}, rate...), nil
	}
	return nil, fmt.Errorf("звук %s пересобрать нечем", i.ACodec)
}

// dubFilter — оригинальный звук приглушается на время фраз, поверх звучит озвучка.
// Слишком длинная фраза ускоряется (не больше чем в 1.6 раза), чтобы уложиться до следующей.
func dubFilter(ctx context.Context, info *Info, o Options) (string, string, error) {
	duck := o.Duck
	if duck <= 0 {
		duck = 0.25
	}
	layout := "stereo"
	rate := info.ARate
	if info.AChannels == 1 {
		layout = "mono"
	}
	if rate == 0 {
		rate = 48000
	}
	var f []string
	base := "[bg]"
	if info.Audio > 0 {
		var on []string
		for _, d := range o.Dubs {
			on = append(on, fmt.Sprintf("between(t,%.3f,%.3f)", d.Start, d.End))
		}
		f = append(f, fmt.Sprintf("[0:a:0]aformat=sample_rates=%d:channel_layouts=%s,volume=%.2f:enable='%s'[bg]", rate, layout, duck, strings.Join(on, "+")))
	} else {
		f = append(f, fmt.Sprintf("anullsrc=r=%d:cl=%s,atrim=0:%.3f[bg]", rate, layout, info.Duration))
	}
	labels := []string{base}
	for k, d := range o.Dubs {
		dur, err := duration(ctx, d.Wav)
		if err != nil {
			return "", "", err
		}
		chain := ""
		if slot := d.End - d.Start; slot > 0 && dur > slot*1.05 {
			chain = fmt.Sprintf("atempo=%.3f,", math.Min(dur/slot, 1.6))
		}
		ms := int64(d.Start * 1000)
		f = append(f, fmt.Sprintf("[%d:a]%saformat=sample_rates=%d:channel_layouts=%s,adelay=%d:all=1[d%d]", k+1, chain, rate, layout, ms, k))
		labels = append(labels, fmt.Sprintf("[d%d]", k))
	}
	f = append(f, fmt.Sprintf("%samix=inputs=%d:normalize=0:duration=first[a]", strings.Join(labels, ""), len(labels)))
	return strings.Join(f, ";"), "[a]", nil
}

// duration — длина звукового файла в секундах.
func duration(ctx context.Context, path string) (float64, error) {
	p, err := ffmpeg.ProbePath()
	if err != nil {
		return 0, err
	}
	out, err := exec.CommandContext(ctx, p, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), exitText(err))
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// ASS — субтитры: белый текст с чёрной обводкой внизу кадра, размер — от высоты кадра.
func ASS(subs []Sub, w, h int, font string) string {
	if w == 0 || h == 0 {
		w, h = 1920, 1080
	}
	size := max(h*55/1000, 14)
	outline := max(float64(h)/360, 1.5)
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 0\nScaledBorderAndShadow: yes\n\n", w, h)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,&H96000000,-1,0,0,0,100,100,0,0,1,%.1f,%.1f,2,%d,%d,%d,1\n\n",
		font, size, outline, outline/2, w/20, w/20, h/18)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	esc := strings.NewReplacer("\r", "", "\n", `\N`, "{", "(", "}", ")")
	for _, s := range subs {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", assTime(s.Start), assTime(s.End), esc.Replace(strings.TrimSpace(s.Text)))
	}
	return b.String()
}

func assTime(t float64) string {
	cs := int64(math.Round(math.Max(t, 0) * 100))
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

// run запускает ffmpeg; progress — доля готового по времени ролика.
func run(ctx context.Context, dir string, progress func(float64), total float64, args ...string) error {
	p, err := ffmpeg.Path()
	if err != nil {
		return err
	}
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-progress", "pipe:1"}, args...)
	cmd := exec.CommandContext(ctx, p, full...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && k == "out_time_us" && progress != nil && total > 0 {
			us, _ := strconv.ParseFloat(v, 64)
			progress(math.Min(us/1e6/total, 1))
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 && len(msg) > 400 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return nil
}

func exitText(err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
