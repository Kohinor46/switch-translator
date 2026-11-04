// zst сжимает файл в zstd — так ffmpeg вшивается в программу (go run ./tools/zst вход выход).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "использование: zst вход выход.zst")
		os.Exit(2)
	}
	in, err := os.Open(os.Args[1])
	check(err)
	defer in.Close()
	out, err := os.Create(os.Args[2])
	check(err)
	enc, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	check(err)
	_, err = io.Copy(enc, in)
	check(err)
	check(enc.Close())
	check(out.Close())
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
