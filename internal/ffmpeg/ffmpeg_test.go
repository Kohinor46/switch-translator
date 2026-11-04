package ffmpeg

import (
	"bytes"
	"os"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

// Распаковка из вшитых файлов: подменяем их маленьким «ffmpeg».
func TestExtract(t *testing.T) {
	want := []byte("#!/bin/sh\necho ok\n")
	var z bytes.Buffer
	enc, _ := zstd.NewWriter(&z)
	enc.Write(want)
	enc.Close()
	old := binFS
	defer func() { binFS = old; paths = map[string]string{} }()
	binFS = fstest.MapFS{platformDir() + "/ffmpeg.zst": {Data: z.Bytes()}}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	if !Available() {
		t.Fatal("не видно вшитого ffmpeg")
	}
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, want) {
		t.Fatal("распаковано не то")
	}
	if st, _ := os.Stat(p); st.Mode()&0o100 == 0 {
		t.Fatal("не исполняемый")
	}
	if _, err := ProbePath(); err != ErrMissing {
		t.Fatalf("ffprobe: %v", err)
	}
}

// Настоящий ffmpeg, если он вшит: запускается и показывает версию.
func TestRealFFmpeg(t *testing.T) {
	if !Available() {
		t.Skip(ErrMissing)
	}
	cmd, err := Command("-version")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(bytes.SplitN(out, []byte("\n"), 2)[0]))
}
