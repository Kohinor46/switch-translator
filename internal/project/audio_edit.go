package project

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"switch-translator/internal/audio"
	"switch-translator/internal/speech"
)

// Своя озвучка: запись с микрофона или готовый файл. Хранится в audio/dub/<имя>.own.wav
// и при сборке вписывается в игру так же, как озвучка голосом.

// OwnDubPath — куда положить свою озвучку файла i.
func (s *Session) OwnDubPath(i int) string {
	return filepath.Join(s.WorkDir, "audio", "dub", s.audioBase(s.Audio[i].File)+".own.wav")
}

// ImportDub берёт свою озвучку из файла src (WAV, MP3, OGG, FLAC, M4A — что прочтёт ffmpeg,
// и звуки Nintendo) и переводит её в WAV.
func (s *Session) ImportDub(ctx context.Context, i int, src string) error {
	dst := s.OwnDubPath(i)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp.wav"
	defer os.Remove(tmp)
	if err := speech.DecodeToWav(ctx, src, tmp); err != nil {
		return fmt.Errorf("не удалось прочитать %s: %w", filepath.Base(src), err)
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	p, err := audio.ReadWAV(b)
	if err != nil {
		return fmt.Errorf("не удалось прочитать %s: %w", filepath.Base(src), err)
	}
	if p.Seconds() < 0.05 {
		return fmt.Errorf("в %s нет звука", filepath.Base(src))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	l := &s.Audio[i]
	l.Dub, l.HandDub, l.Error = dst, true, ""
	s.log("%s: своя озвучка, %.1f с", path.Base(l.File), p.Seconds())
	return s.SaveAudio()
}

// ClearDub убирает озвучку файла i — в игре останется оригинал.
func (s *Session) ClearDub(i int) error {
	l := &s.Audio[i]
	if l.HandDub && l.Dub != "" {
		os.Remove(l.Dub)
	}
	l.Dub, l.HandDub = "", false
	return s.SaveAudio()
}

// AudioSeconds — длительность оригинала.
func (s *Session) AudioSeconds(ctx context.Context, i int) (float64, error) {
	p, err := s.decodeAudio(ctx, s.Audio[i].File)
	if err != nil {
		return 0, err
	}
	return p.Seconds(), nil
}

// EditSpeech — ручная правка речи и перевода файла i. Новый текст речи без нового перевода
// сбрасывает автоматический перевод: он был сделан по старому тексту. Новый перевод сбрасывает
// озвучку голосом (своя запись остаётся).
func (s *Session) EditSpeech(i int, text, translation string) error {
	l := &s.Audio[i]
	textChanged, trChanged := text != l.Transcript, translation != l.Translation
	if textChanged {
		l.Transcript, l.Recognized, l.HandText, l.Error = text, true, true, ""
	}
	switch {
	case trChanged:
		l.Translation, l.HandTranslation = translation, translation != ""
	case textChanged && !l.HandTranslation:
		l.Translation = "" // переведут заново, по новому тексту
	}
	if l.Translation != translation || trChanged {
		if !l.HandDub {
			l.Dub = ""
		}
	}
	if text != "" {
		l.Selected = true
	}
	return s.SaveAudio()
}
