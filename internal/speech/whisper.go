package speech

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"switch-translator/internal/i18n"
)

type WhisperModel struct {
	Name string
	Size string
	Note string
}

// WhisperModels — модели whisper.cpp, от быстрой к точной.
var WhisperModels = []WhisperModel{
	{"base", "142 МБ", "быстрая, ошибается чаще"},
	{"small", "466 МБ", "для слабого компьютера"},
	{"large-v3-turbo", "1,6 ГБ", "лучший выбор для игр"},
	{"large-v3", "3,1 ГБ", "самая точная, медленная"},
}

func (m WhisperModel) Label() string {
	return fmt.Sprintf("%s — %s, %s", m.Name, i18n.T(m.Size), i18n.T(m.Note))
}

// WhisperModelPath: имя модели → файл в ~/.cache/whisper; полный путь возвращается как есть.
func WhisperModelPath(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.Index(name, " — "); i >= 0 {
		name = name[:i]
	}
	if strings.ContainsAny(name, `/\`) || strings.HasSuffix(name, ".bin") {
		return name
	}
	return filepath.Join(home(), ".cache", "whisper", "ggml-"+name+".bin")
}

func WhisperModelURL(name string) string {
	return "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-" + name + ".bin"
}

// DownloadWhisperModel скачивает модель в ~/.cache/whisper; прерванная загрузка не оставляет битый файл.
func DownloadWhisperModel(ctx context.Context, name string, progress func(done, total int64)) error {
	if i := strings.Index(name, " — "); i >= 0 {
		name = name[:i]
	}
	if err := downloadFile(ctx, WhisperModelURL(name), WhisperModelPath(name), progress); err != nil {
		return fmt.Errorf("скачивание модели %s: %w", name, err)
	}
	return nil
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil && n > 0 {
		p.fn(p.done, p.total)
	}
	return n, err
}
