package video

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"switch-translator/internal/bffnt"
	"switch-translator/internal/ffmpeg"
)

// Синтетические ролики: MP4 (H.264 + AAC) и WebM (VP9 + Opus). После пересборки
// с субтитрами и озвучкой кодеки, размер кадра, частота и длина должны остаться прежними.
func TestRender(t *testing.T) {
	if !ffmpeg.Available() {
		t.Skip(ffmpeg.ErrMissing)
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, c := range []struct{ name, vc, ac string }{
		{"a.mp4", "libx264", "aac"},
		{"b.webm", "libvpx-vp9", "libopus"},
	} {
		src := filepath.Join(dir, c.name)
		err := run(ctx, "", nil, 0, "-y", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=4",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=4:sample_rate=48000", "-ac", "2",
			"-c:v", c.vc, "-b:v", "800k", "-c:a", c.ac, "-shortest", src)
		if err != nil {
			t.Fatal(err)
		}
		dub := filepath.Join(dir, "dub.wav")
		if err := run(ctx, "", nil, 0, "-y", "-f", "lavfi", "-i", "sine=frequency=880:duration=2.5:sample_rate=22050", dub); err != nil {
			t.Fatal(err)
		}
		info, err := Probe(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out-"+c.name)
		var last float64
		err = Render(ctx, src, out, info, Options{
			Subs:     []Sub{{0.5, 2.5, "Привет, мир!\nВторая строка"}},
			Font:     bffnt.DefaultTTF(),
			FontName: "Noto Sans",
			Dubs:     []Dub{{Start: 1, End: 3, Wav: dub}},
		}, func(f float64) { last = f })
		if err != nil {
			t.Fatal(err)
		}
		got, err := Probe(ctx, out)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s → %s (прогресс %.2f)", c.name, info.Describe(), got.Describe(), last)
		if got.VCodec != info.VCodec || got.Width != info.Width || got.Height != info.Height || got.FPS != info.FPS ||
			got.ACodec != info.ACodec || math.Abs(got.Duration-info.Duration) > 0.1 {
			t.Errorf("параметры изменились: %+v → %+v", info, got)
		}
		if c.name == "a.mp4" && os.Getenv("SWTR_FRAME") != "" {
			exec.Command("cp", out, os.Getenv("SWTR_FRAME")).Run()
		}
	}
}

func TestASS(t *testing.T) {
	s := ASS([]Sub{{61.5, 63.25, "Да {нет}\nок"}}, 1280, 720, "Noto Sans")
	want := "Dialogue: 0,0:01:01.50,0:01:03.25,Default,,0,0,0,,Да (нет)\\Nок\n"
	if len(s) < len(want) || s[len(s)-len(want):] != want {
		t.Fatalf("строка субтитров: %q", s)
	}
}
