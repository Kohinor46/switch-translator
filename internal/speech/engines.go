package speech

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Общие для всех голосов действия: список голосов, готовность локального голоса, его установка,
// копирование голоса по образцам.

func labels(vs []struct{ ID, Name string }) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = VoiceLabel(v.Name, v.ID)
	}
	return out
}

// Voices — голоса для выбора. Для Azure, ElevenLabs, OpenAI и системных голосов списки отдельные.
func Voices(ctx context.Context, id string, cfg Config) ([]string, error) {
	switch id {
	case "piper":
		var out []string
		for _, v := range piperVoices {
			out = append(out, VoiceLabel(v.Label, v.ID))
		}
		return out, nil
	case "silero":
		var out []string
		for _, v := range sileroVoices {
			out = append(out, VoiceLabel(v.Label, v.ID))
		}
		return out, nil
	case "xtts":
		return xttsVoices(), nil
	case "rhvoice":
		return rhvoiceVoices(), nil
	case "yandex":
		return labels(yandexVoices), nil
	case "salute":
		return labels(saluteVoices), nil
	case "gemini":
		return labels(geminiVoices), nil
	case "google":
		return googleVoices(ctx, cfg.APIKey, cfg.Lang)
	case "cartesia":
		return cartesiaVoices(ctx, cfg.APIKey, cfg.Lang)
	}
	return nil, nil
}

// Ready — готов ли локальный голос и что про него сказать (для голосов без установки — true, "").
func Ready(id string, cfg Config) (bool, string) {
	switch id {
	case "piper":
		if !fileExists(sherpaExe()) {
			return false, "Piper ещё не скачан (около 20 МБ) и голос — около 65 МБ."
		}
		if piperModel(cfg.Model) == "" {
			return false, "Голос «" + piperName(cfg.Model) + "» ещё не скачан (около 65 МБ)."
		}
		return true, "голос " + piperName(cfg.Model)
	case "silero":
		if !pythonReady("silero") || !fileExists(sileroPath()) {
			return false, "Silero ещё не установлен: нужен Python, программа поставит torch (около 1 ГБ) и модель (150 МБ)."
		}
		return true, "модель " + sileroModel
	case "xtts":
		if !pythonReady("xtts") {
			return false, "XTTS ещё не установлен: нужен Python, программа поставит torch и coqui-tts, модель — около 1,9 ГБ."
		}
		return true, "XTTS-v2 на этом компьютере"
	}
	return true, ""
}

// Setup скачивает или ставит то, что нужно локальному голосу.
func Setup(ctx context.Context, id string, cfg Config, log func(string), progress func(done, total int64)) error {
	switch id {
	case "piper":
		return setupPiper(ctx, cfg.Model, log, progress)
	case "silero":
		return setupSilero(ctx, log, progress)
	case "xtts":
		return setupXTTS(ctx, log)
	}
	return nil
}

// CloneVoice создаёт голос по образцам речи и возвращает его подпись для списка голосов.
func CloneVoice(ctx context.Context, id string, cfg Config, name string, samples []string) (string, error) {
	switch id {
	case "elevenlabs":
		vid, err := cloneElevenLabs(ctx, cfg.APIKey, name, samples)
		return VoiceLabel(name, vid), err
	case "cartesia":
		return cloneCartesia(ctx, cfg.APIKey, cfg.Lang, name, samples)
	case "xtts":
		return cloneXTTS(name, samples)
	}
	return "", fmt.Errorf("эта озвучка копировать голос не умеет")
}

func newLocalOrCloud(id string, cfg Config) (Synthesizer, error) {
	need := func(what string) error { return fmt.Errorf("%s: нужен ключ", what) }
	switch id {
	case "piper":
		return &piperTTS{cfg}, nil
	case "silero":
		return &sileroTTS{cfg}, nil
	case "xtts":
		return &xttsTTS{cfg}, nil
	case "rhvoice":
		if runtime.GOOS == "darwin" {
			return nil, fmt.Errorf("RHVoice для macOS нет — выбери Piper или Silero")
		}
		return &rhvoiceTTS{cfg}, nil
	case "yandex":
		if cfg.APIKey == "" {
			return nil, need("Яндекс SpeechKit")
		}
		return &yandexTTS{cfg}, nil
	case "salute":
		if cfg.APIKey == "" {
			return nil, need("SaluteSpeech")
		}
		return &saluteTTS{cfg}, nil
	case "gemini":
		if cfg.APIKey == "" {
			return nil, need("Gemini")
		}
		return &geminiTTS{cfg}, nil
	case "google":
		if cfg.APIKey == "" {
			return nil, need("Google Cloud")
		}
		return &googleTTS{cfg}, nil
	case "cartesia":
		if cfg.APIKey == "" {
			return nil, need("Cartesia")
		}
		return &cartesiaTTS{cfg}, nil
	}
	return nil, fmt.Errorf("неизвестная озвучка %q", id)
}

// CanClone — умеет ли озвучка копировать голос по образцам.
func CanClone(id string) bool {
	for _, o := range TTSOptions() {
		if o.ID == id {
			return o.Clone
		}
	}
	return false
}

// CloneSeconds — сколько секунд образцов брать для копирования голоса: XTTS хватает полуминуты
// (и каждая фраза с длинными образцами озвучивается дольше), Cartesia берёт до минуты, ElevenLabs — до трёх.
func CloneSeconds(id string) float64 {
	switch id {
	case "xtts":
		return 30
	case "cartesia":
		return 60
	}
	return 120
}

// VoiceUsable — есть ли ещё такой голос: образцы скопированного голоса XTTS могли удалить.
func VoiceUsable(id, label string) bool {
	if id == "xtts" {
		if name, ok := strings.CutPrefix(VoiceID(label), "clone:"); ok {
			return fileExists(filepath.Join(xttsVoicesDir(), name))
		}
	}
	return label != ""
}
