package speech

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"switch-translator/internal/audio"
)

// Помощник Python: ответы с пометкой находятся среди постороннего вывода библиотек,
// ошибка скрипта приходит понятным текстом, упавший помощник перезапускается.
func TestPyWorker(t *testing.T) {
	sys, err := exec.LookPath("python3")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("нет python3")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), ".cache"))
	if err := os.MkdirAll(filepath.Dir(venvPython()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sys, venvPython()); err != nil {
		t.Fatal(err)
	}
	defer StopWorkers()
	script := `
import sys, json
print("Loading model... noise on stdout")
for line in sys.stdin:
    r = json.loads(line)
    if r.get("die"):
        sys.exit(3)
    if r.get("bad"):
        sys.stdout.write("@@SWTR " + json.dumps({"error": "плохая фраза"}) + "\n"); sys.stdout.flush(); continue
    print("> splitting text")
    sys.stdout.write("@@SWTR " + json.dumps({"ok": True, "echo": r["text"].upper()}) + "\n"); sys.stdout.flush()
`
	ctx := context.Background()
	for _, want := range []string{"ПРИВЕТ", "ЕЩЁ"} {
		m, err := pyCall(ctx, "t", script, nil, map[string]string{"text": strings.ToLower(want)})
		if err != nil || m["echo"] != want {
			t.Fatalf("%v %v", m, err)
		}
	}
	if _, err := pyCall(ctx, "t", script, nil, map[string]bool{"bad": true}); err == nil || err.Error() != "плохая фраза" {
		t.Errorf("ошибка скрипта: %v", err)
	}
	if _, err := pyCall(ctx, "t", script, nil, map[string]bool{"die": true}); err == nil {
		t.Error("упавший помощник не заметили")
	}
	if m, err := pyCall(ctx, "t", script, nil, map[string]string{"text": "снова"}); err != nil || m["echo"] != "СНОВА" {
		t.Errorf("помощник не перезапустился: %v %v", m, err)
	}
}

func TestWriteAudio(t *testing.T) {
	dir := t.TempDir()
	raw := []byte{1, 0, 0xFF, 0x7F, 0, 0x80}
	if err := writeAudio(filepath.Join(dir, "a.wav"), raw, 24000); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.wav"))
	p, err := audio.ReadWAV(b)
	if err != nil || p.Rate != 24000 || p.Samples() != 3 || p.Channels[0][1] != 32767 || p.Channels[0][2] != -32768 {
		t.Fatalf("%v %+v", err, p)
	}
	// готовый WAV пишется как есть
	writeAudio(filepath.Join(dir, "b.wav"), b, 8000)
	if b2, _ := os.ReadFile(filepath.Join(dir, "b.wav")); !bytes.Equal(b, b2) {
		t.Error("WAV переписан")
	}
}

func TestVoiceNames(t *testing.T) {
	for in, want := range map[string]string{"": "ru_RU-irina-medium", "Денис, мужской — denis": "ru_RU-denis-medium", "en_US-amy-medium": "en_US-amy-medium"} {
		if got := piperName(in); got != want {
			t.Errorf("piperName(%q) = %q", in, got)
		}
	}
	if locale("ru") != "ru-RU" || locale("en") != "en-US" || locale("pt-PT") != "pt-PT" {
		t.Error("locale")
	}
	for _, id := range []string{"piper", "silero", "xtts", "yandex", "salute", "gemini"} {
		if v, _ := Voices(context.Background(), id, Config{}); len(v) == 0 {
			t.Errorf("у %s нет голосов", id)
		}
	}
	if l, err := cloneXTTS("Тренер/Ring!", []string{writeTestWav(t)}); err != nil || VoiceID(l) != "clone:ТренерRing" {
		t.Errorf("%q %v", l, err)
	}
}

func writeTestWav(t *testing.T) string {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), ".cache"))
	p := filepath.Join(t.TempDir(), "s.wav")
	os.WriteFile(p, audio.WAV(&audio.PCM{Rate: 16000, Channels: [][]int16{make([]int16, 160)}}), 0o644)
	return p
}

// Распаковка .tar.bz2: без верхней папки, ссылки внутри архива сохраняются, пути наружу отбрасываются.
func TestUntarBz2(t *testing.T) {
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("нет bzip2")
	}
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		tw.WriteHeader(h)
		tw.Write([]byte(body))
	}
	add(&tar.Header{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "top/bin/tool", Typeflag: tar.TypeReg, Mode: 0o755}, "#!/bin/sh\n")
	add(&tar.Header{Name: "top/lib/libx.1.dylib", Typeflag: tar.TypeReg, Mode: 0o644}, "lib")
	add(&tar.Header{Name: "top/lib/libx.dylib", Typeflag: tar.TypeSymlink, Linkname: "libx.1.dylib"}, "")
	add(&tar.Header{Name: "top/../../evil", Typeflag: tar.TypeReg, Mode: 0o644}, "x")
	tw.Close()
	cmd := exec.Command("bzip2", "-c")
	cmd.Stdin = &tb
	bz, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "out")
	if err := untarBz2(bytes.NewReader(bz), dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dst, "bin", "tool")); err != nil || st.Mode()&0o100 == 0 {
		t.Errorf("программа: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "lib", "libx.dylib")); err != nil || string(b) != "lib" {
		t.Errorf("ссылка: %v", err)
	}
	if fileExists(filepath.Join(dir, "evil")) {
		t.Error("файл вылез наружу")
	}
}
