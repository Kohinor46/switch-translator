package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"switch-translator/internal/bffnt"
	"switch-translator/internal/engine"
	"switch-translator/internal/nx"
	"switch-translator/internal/speech"
	"switch-translator/internal/translate"
	"switch-translator/internal/video"
)

// VideoLine — ролик игры и что с ним сделано.
type VideoLine struct {
	File        string         `json:"file"`
	Size        int64          `json:"size"`
	Format      string         `json:"format"`
	Selected    bool           `json:"selected"`
	Unsupported string         `json:"unsupported,omitempty"` // почему ролик не пересобрать
	Info        *video.Info    `json:"info,omitempty"`
	Recognized  bool           `json:"recognized,omitempty"`
	Segments    []VideoSegment `json:"segments,omitempty"`
	Subtitles   bool           `json:"subtitles"` // вшить субтитры перевода
	Dubbing     bool           `json:"dubbing"`   // подмешать озвучку перевода
	Output      string         `json:"output,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// VideoSegment — фраза ролика: время, оригинал, перевод, озвучка.
type VideoSegment struct {
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Text        string  `json:"text"`
	Translation string  `json:"translation,omitempty"`
	Dub         string  `json:"dub,omitempty"`
}

// HasSpeech — после распознавания: есть ли в ролике речь.
func (l *VideoLine) HasSpeech() bool { return len(l.Segments) > 0 }

// Translated — сколько фраз переведено.
func (l *VideoLine) Translated() int {
	n := 0
	for _, s := range l.Segments {
		if strings.TrimSpace(s.Translation) != "" {
			n++
		}
	}
	return n
}

func (s *Session) videoPath() string { return filepath.Join(s.WorkDir, "video.json") }

// unsupportedVideo — какие форматы пересобрать не выйдет.
func unsupportedVideo(format string) string {
	switch {
	case strings.Contains(format, "Bink"):
		return "Bink: свободного кодировщика нет — ролик можно только посмотреть"
	case strings.Contains(format, "USM"):
		return "CRI USM: ffmpeg такие ролики не собирает"
	case strings.Contains(format, "THP"):
		return "THP: ffmpeg такие ролики не собирает (в портах Galaxy для Switch вместо них играют ролики WebM из data/video_replace)"
	}
	return ""
}

// initVideo собирает список роликов из отчёта и подтягивает прошлую работу из video.json.
func (s *Session) initVideo() {
	prev := map[string]VideoLine{}
	var saved []VideoLine
	if b, err := os.ReadFile(s.videoPath()); err == nil && json.Unmarshal(b, &saved) == nil {
		for _, l := range saved {
			prev[l.File] = l
		}
	}
	s.Video = nil
	for _, f := range s.Report.Of(engine.Video) {
		line, ok := prev[f.Path]
		if !ok {
			line = VideoLine{File: f.Path, Subtitles: true}
		}
		line.Size, line.Format = f.Size, f.Format
		line.Unsupported = unsupportedVideo(f.Format)
		if line.Output != "" && !fileExists(line.Output) {
			line.Output = ""
		}
		s.Video = append(s.Video, line)
	}
	sort.Slice(s.Video, func(i, j int) bool { return s.Video[i].File < s.Video[j].File })
}

func (s *Session) SaveVideo() error { return writeJSON(s.videoPath(), s.Video) }

// VideoSource — ролик игры как файл (извлекается один раз).
func (s *Session) VideoSource(file string) (string, error) {
	dst := filepath.Join(s.WorkDir, "video", "src", s.audioBase(file)+path.Ext(file))
	if fileExists(dst) {
		return dst, nil
	}
	for _, f := range s.Game.RomFS.Files {
		if f.Path == file {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return "", err
			}
			if err := copyOut(s.Game.RomFS.Open(f), dst+".part"); err != nil {
				return "", err
			}
			return dst, os.Rename(dst+".part", dst)
		}
	}
	return "", fmt.Errorf("в игре нет файла %s", file)
}

// videoInfo — параметры ролика (запоминаются в video.json).
func (s *Session) videoInfo(ctx context.Context, l *VideoLine) (string, error) {
	src, err := s.VideoSource(l.File)
	if err != nil {
		return "", err
	}
	if l.Info == nil {
		if l.Info, err = video.Probe(ctx, src); err != nil {
			return "", err
		}
	}
	return src, nil
}

// eachVideo проходит по отмеченным роликам, сохраняя video.json после каждого.
func (s *Session) eachVideo(ctx context.Context, label string, idx []int, progress Progress, fn func(l *VideoLine, part func(float64)) error) error {
	began := time.Now()
	for n, i := range idx {
		if err := ctx.Err(); err != nil {
			return err
		}
		l := &s.Video[i]
		left := Remaining(began, n, len(idx))
		part := func(f float64) { progress(label+": "+path.Base(l.File)+left, float64(n)+f, float64(len(idx))) }
		part(0)
		l.Error = ""
		if err := fn(l, part); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			l.Error = err.Error()
			s.log("%s: %v", path.Base(l.File), err)
		}
		if err := s.SaveVideo(); err != nil {
			return err
		}
	}
	progress(label, float64(len(idx)), float64(len(idx)))
	return nil
}

// RecognizeVideo распознаёт речь роликов фразами со временем.
func (s *Session) RecognizeVideo(ctx context.Context, stt speech.Recognizer, idx []int, progress Progress) error {
	return s.eachVideo(ctx, "Распознавание речи", idx, progress, func(l *VideoLine, _ func(float64)) error {
		src, err := s.videoInfo(ctx, l)
		if err != nil {
			return err
		}
		l.Segments, l.Recognized = nil, true
		if l.Info.Audio == 0 {
			s.log("%s: в ролике нет звука", path.Base(l.File))
			return nil
		}
		wav := filepath.Join(s.WorkDir, "video", "wav", s.audioBase(l.File)+".wav")
		if err := os.MkdirAll(filepath.Dir(wav), 0o755); err != nil {
			return err
		}
		if !fileExists(wav) {
			if err := video.ExtractAudio(ctx, src, wav); err != nil {
				return err
			}
		}
		segs, err := speech.Segments(ctx, stt, wav, l.Info.Duration)
		if err != nil {
			return err
		}
		for _, sg := range segs {
			text, _ := speech.CleanTranscript(sg.Text)
			l.Segments = append(l.Segments, VideoSegment{Start: sg.Start, End: sg.End, Text: text})
		}
		if len(segs) == 0 {
			s.log("%s: речи нет", path.Base(l.File))
		} else {
			s.log("%s: %d фраз", path.Base(l.File), len(segs))
		}
		return nil
	})
}

// TranslateVideo переводит фразы роликов (уже переведённые не трогает).
func (s *Session) TranslateVideo(ctx context.Context, tr translate.Translator, idx []int, progress Progress) error {
	return s.eachVideo(ctx, "Перевод речи", idx, progress, func(l *VideoLine, _ func(float64)) error {
		var todo []int
		var texts []string
		for k, sg := range l.Segments {
			if sg.Translation == "" && sg.Text != "" {
				todo = append(todo, k)
				texts = append(texts, sg.Text)
			}
		}
		if len(texts) == 0 {
			return nil
		}
		res, err := tr.Translate(ctx, texts)
		if err != nil {
			return err
		}
		for j, k := range todo {
			if j < len(res) {
				l.Segments[k].Translation = res[j]
			}
		}
		return nil
	})
}

// DubVideo озвучивает переведённые фразы роликов.
func (s *Session) DubVideo(ctx context.Context, tts speech.Synthesizer, idx []int, progress Progress) error {
	dir := filepath.Join(s.WorkDir, "video", "dub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return s.eachVideo(ctx, "Озвучка", idx, progress, func(l *VideoLine, part func(float64)) error {
		for k := range l.Segments {
			sg := &l.Segments[k]
			if sg.Translation == "" {
				continue
			}
			out, err := tts.Synthesize(ctx, speech.ShortenStretched(sg.Translation), filepath.Join(dir, fmt.Sprintf("%s_%03d", s.audioBase(l.File), k)))
			if err != nil {
				return err
			}
			sg.Dub = out
			part(float64(k+1) / float64(len(l.Segments)))
		}
		l.Dubbing = true
		return nil
	})
}

// RenderVideo пересобирает ролики: субтитры и (или) озвучка перевода.
func (s *Session) RenderVideo(ctx context.Context, idx []int, progress Progress) error {
	dir := filepath.Join(s.WorkDir, "video", "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return s.eachVideo(ctx, "Сборка роликов", idx, progress, func(l *VideoLine, part func(float64)) error {
		if l.Unsupported != "" {
			return fmt.Errorf("%s", l.Unsupported)
		}
		if l.Translated() == 0 {
			return fmt.Errorf("нет перевода — сначала распознай и переведи речь")
		}
		if !l.Subtitles && !l.Dubbing {
			return fmt.Errorf("выбери, что делать с роликом: субтитры, озвучку или и то и другое")
		}
		src, err := s.videoInfo(ctx, l)
		if err != nil {
			return err
		}
		var o video.Options
		if l.Subtitles {
			o.Font, o.FontName = bffnt.DefaultTTF(), "Noto Sans"
			for _, sg := range l.Segments {
				o.Subs = append(o.Subs, video.Sub{Start: sg.Start, End: sg.End, Text: sg.Translation})
			}
		}
		if l.Dubbing {
			for k, sg := range l.Segments {
				if sg.Dub == "" || !fileExists(sg.Dub) {
					continue
				}
				end := l.Info.Duration
				if k+1 < len(l.Segments) {
					end = l.Segments[k+1].Start
				}
				o.Dubs = append(o.Dubs, video.Dub{Start: sg.Start, End: end, Wav: sg.Dub})
			}
			if len(o.Dubs) == 0 && !l.Subtitles {
				return fmt.Errorf("озвучки ещё нет — нажми «Озвучить»")
			}
		}
		out := filepath.Join(dir, s.audioBase(l.File)+path.Ext(l.File))
		if err := video.Render(ctx, src, out, l.Info, o, part); err != nil {
			return err
		}
		l.Output = out
		s.log("%s: ролик собран (%s)", path.Base(l.File), nx.HumanSize(fileSize(out)))
		return nil
	})
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// buildVideos кладёт готовые ролики в мод.
func (s *Session) buildVideos(res *BuildResult) error {
	for _, l := range s.Video {
		if !l.Selected || l.Output == "" || !fileExists(l.Output) {
			continue
		}
		data, err := os.ReadFile(l.Output)
		if err != nil {
			return err
		}
		if err := writeFile(s.ModPath(l.File), data); err != nil {
			return err
		}
		res.Files = append(res.Files, l.File)
		res.Videos++
	}
	return nil
}
