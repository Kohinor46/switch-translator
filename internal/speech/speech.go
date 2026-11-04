// Package speech — распознавание речи (STT) и озвучка (TTS) для звука игры.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"switch-translator/internal/i18n"
	"sync"
	"time"

	"switch-translator/internal/audio"
	"switch-translator/internal/ffmpeg"
	"switch-translator/internal/translate"
	"switch-translator/internal/whisper"
)

// Option — способ распознавания или озвучки вместе с инструкцией, как его подключить.
type Option struct {
	ID       string
	Title    string
	Tier     translate.Tier
	NeedsKey bool
	Models   []string // модели или голоса по умолчанию; полный список — Voices / WhisperModels
	Note     string
	Site     string
	Help     string   // markdown
	Commands []string // команды, которые можно скопировать
	Clone    bool     // умеет копировать голос по образцам
	Style    bool     // понимает, как говорить, словами («бодро, как тренер»)
	Setup    string   // локальный голос: надпись кнопки, которая скачивает или ставит нужное ("" — ничего не нужно)
}

func (o Option) Label() string {
	return fmt.Sprintf("%s — %s", i18n.T(o.Title), i18n.T(o.Tier.Title()))
}

// VgmstreamCommand — установка vgmstream на Mac: нужен только для звука не от Nintendo
// (WEM, HCA, FSB, Opus…). Под Windows и Linux программа скачивает его сама.
const VgmstreamCommand = "brew install vgmstream"

type Config struct {
	APIKey string
	Model  string // модель, голос или путь к файлу модели whisper.cpp
	Lang   string
	Region string // регион Azure
	Style  string // как говорить — для озвучки, которая это понимает
}

type Recognizer interface {
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// Segment — кусок речи со временем (секунды): для субтитров и озвучки видео.
type Segment struct {
	Start, End float64
	Text       string
}

// SegmentRecognizer умеет отдавать речь кусками со временем.
type SegmentRecognizer interface {
	Segments(ctx context.Context, wavPath string) ([]Segment, error)
}

// Segments — речь кусками со временем. Если распознаватель времени не знает,
// вся речь — один кусок на всю длину (duration, секунды).
func Segments(ctx context.Context, r Recognizer, wav string, duration float64) ([]Segment, error) {
	if sr, ok := r.(SegmentRecognizer); ok {
		return sr.Segments(ctx, wav)
	}
	text, err := r.Transcribe(ctx, wav)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil, err
	}
	return []Segment{{Start: 0, End: duration, Text: strings.TrimSpace(text)}}, nil
}

type Synthesizer interface {
	// Synthesize пишет озвучку в outBase + расширение и возвращает итоговый путь.
	Synthesize(ctx context.Context, text, outBase string) (string, error)
}

func STTOptions() []Option {
	return []Option{
		{ID: "whispercpp", Title: "Whisper на этом компьютере", Tier: translate.Free,
			Note: "Распознаёт речь прямо на компьютере: бесплатно, звук никуда не уходит. Whisper встроен в программу — ставить ничего не нужно.",
			Site: "https://github.com/ggml-org/whisper.cpp",
			Help: "Выбери модель и нажми **«Скачать модель»** — это единственное, что нужно сделать один раз. " +
				"Модель — это файл с «знаниями» распознавателя: чем больше, тем точнее и медленнее. " +
				"Для игр хватает **large-v3-turbo** (1,6 ГБ); на слабом компьютере — **small** или **base**.\n\n" +
				"Модели берутся с huggingface.co/ggerganov/whisper.cpp и лежат в папке ~/.cache/whisper."},
		{ID: "openai-stt", Title: "OpenAI (gpt-4o-transcribe)", Tier: translate.Paid, NeedsKey: true,
			Models: []string{"gpt-4o-transcribe", "whisper-1"},
			Note:   "Распознавание в облаке OpenAI. Оплата за минуты звука.",
			Site:   "https://platform.openai.com/api-keys",
			Help:   openAIKeyHelp},
	}
}

const openAIKeyHelp = "1. Войди на **platform.openai.com** (тот же аккаунт, что в ChatGPT).\n" +
	"2. **Settings → Billing**: пополни баланс — API оплачивается отдельно от подписки ChatGPT.\n" +
	"3. **API keys → Create new secret key**, скопируй ключ.\n\n" +
	"Codex и приложение ChatGPT на компьютере распознавать и озвучивать звук для других программ не умеют — для голоса нужен именно API-ключ."

func TTSOptions() []Option {
	return []Option{
		{ID: "none", Title: "Без озвучки (только текст перевода)", Tier: translate.Free},
		{ID: "azure", Title: "Голоса Microsoft Azure", Tier: translate.FreeTier, NeedsKey: true,
			Note: "Живые нейросетевые голоса Microsoft. Бесплатный тариф — 500 тысяч символов в месяц.",
			Site: "https://portal.azure.com/#create/Microsoft.CognitiveServicesSpeechServices",
			Help: "1. Зарегистрируйся на **portal.azure.com** (бесплатный аккаунт; при регистрации Microsoft просит карту для проверки, " +
				"на бесплатном тарифе деньги не списываются).\n" +
				"2. Создай ресурс **Speech** (кнопка «Сайт» ниже открывает его создание), тариф **Free F0**, регион — например **West Europe**.\n" +
				"3. Открой ресурс → **Keys and Endpoint**, скопируй **KEY 1** в поле «Ключ», а **Location/Region** (например, westeurope) — в поле «Регион».\n" +
				"4. Список голосов загрузится сам. Для русского — **Светлана** (женский) и **Дмитрий** (мужской)."},
		{ID: "windows", Title: "Голоса Windows", Tier: translate.Free,
			Note: "Голоса, встроенные в Windows: бесплатно и без интернета. Только под Windows.",
			Help: "Русский голос **Irina** ставится с русским языковым пакетом: **Параметры → Время и язык → Язык → Русский → Речь**. " +
				"После установки голос появится в списке."},
		{ID: "say", Title: "Голоса macOS", Tier: translate.Free,
			Note: "Встроенные голоса Mac: бесплатно, но звучат как синтезатор.",
			Help: "Русских голосов по умолчанию нет — их нужно один раз скачать:\n\n" +
				"**Системные настройки → Универсальный доступ → Устная речь (Spoken Content) → Системный голос → Управлять голосами…** " +
				"→ Русский → отметь **Milena** или **Yuri** (лучше версии «улучшенный»).\n\n" +
				"После этого голос появится в списке — нажми «Прослушать», чтобы выбрать."},
		{ID: "piper", Title: "Piper на этом компьютере", Tier: translate.Free, Setup: "Скачать Piper и голос",
			Note: "Лёгкие нейросетевые голоса без интернета: быстро, бесплатно, звук никуда не уходит.",
			Site: "https://github.com/k2-fsa/sherpa-onnx",
			Help: "Выбери голос и нажми **«Скачать Piper и голос»** — больше ничего ставить не нужно. " +
				"Программа скачает sherpa-onnx (около 20 МБ, он запускает голоса Piper) и сам голос (около 65 МБ); каждый голос скачивается один раз.\n\n" +
				"Русские голоса: **Ирина**, **Денис**, **Дмитрий**, **Руслан**. Голос Руслана — только для некоммерческого использования. " +
				"Для другого языка впиши полное имя голоса Piper, например **en_US-amy-medium**."},
		{ID: "silero", Title: "Silero на этом компьютере", Tier: translate.Free, Setup: "Установить Silero",
			Note: "Лучшее бесплатное звучание русского: ударения и «ё» ставятся сами. Без интернета, только русский.",
			Site: "https://github.com/snakers4/silero-models",
			Help: "Нужен **Python 3.10–3.12** (python.org). Нажми **«Установить Silero»** — программа сделает для голосов своё окружение Python " +
				"(системный Python не трогается), поставит туда torch (около 1 ГБ) и скачает модель " + sileroModel + " (150 МБ).\n\n" +
				"Голоса: **Ксения**, **Бая**, **Айдар**, **Евгений**. Лицензия моделей Silero — **CC BY-NC 4.0**: только некоммерческое использование. " +
				"Латиницу и цифры Silero читает плохо — их лучше писать в переводе словами."},
		{ID: "xtts", Title: "XTTS-v2 на этом компьютере", Tier: translate.Free, Clone: true, Setup: "Установить XTTS (лицензия CPML)",
			Note: "Говорит голосом персонажа игры: копирует его по нескольким репликам. Без интернета, но медленно без видеокарты NVIDIA.",
			Site: "https://github.com/idiap/coqui-ai-TTS",
			Help: "Нужен **Python 3.10–3.12** (python.org). Нажми **«Установить XTTS»** — программа сделает для голосов своё окружение Python, " +
				"поставит torch и coqui-tts и скачает модель XTTS-v2 (около 1,9 ГБ).\n\n" +
				"**Лицензия:** модель XTTS-v2 распространяется по **Coqui Public Model License (CPML)** — только для некоммерческого использования. " +
				"Нажимая «Установить XTTS», ты соглашаешься с ней.\n\n" +
				"**Голос персонажа:** отметь справа несколько чистых реплик одного персонажа (хватает 10–30 секунд) и нажми «Скопировать голос из отмеченных». " +
				"Голос появится в списке с пометкой «скопированный». Есть и 58 готовых голосов — они тоже говорят по-русски.\n\n" +
				"**Несколько персонажей:** на вкладке «Речь в текст» нажми «Разделить по голосам» — тогда при озвучке голос каждого " +
				"персонажа скопируется по его же репликам, и все его реплики прозвучат его голосом.\n\n" +
				"На Mac модель работает на процессоре: фраза озвучивается несколько секунд."},
		{ID: "rhvoice", Title: "RHVoice", Tier: translate.Free,
			Note: "Лёгкий классический синтез: работает даже на слабом компьютере, но звучит роботизированно. Windows и Linux.",
			Site: "https://github.com/RHVoice/RHVoice",
			Help: "**Windows:** скачай установщики русских голосов со страницы RHVoice (Aleksandr, Anna, Elena, Irina…) — " +
				"голоса встанут в систему и появятся в списке.\n\n" +
				"**Linux:** поставь пакеты rhvoice и rhvoice-russian.\n\n" +
				"**macOS:** RHVoice для Mac нет — выбери Piper или Silero.",
			Commands: []string{"sudo apt install rhvoice rhvoice-russian"}},
		{ID: "openai-tts", Title: "OpenAI TTS", Tier: translate.Paid, NeedsKey: true,
			Note: "Естественные голоса. Оплата за объём текста.",
			Site: "https://platform.openai.com/api-keys",
			Help: openAIKeyHelp + "\n\nКопировать голос из игры OpenAI не умеет — для этого есть ElevenLabs."},
		{ID: "elevenlabs", Title: "ElevenLabs", Tier: translate.Paid, NeedsKey: true, Clone: true,
			Note: "Самые живые голоса и копирование голоса персонажа из игры.",
			Site: "https://elevenlabs.io/app/settings/api-keys",
			Help: "1. Зарегистрируйся на **elevenlabs.io** — есть бесплатный план с небольшим месячным лимитом.\n" +
				"2. Открой **Settings → API Keys → Create API Key**, скопируй ключ и вставь сюда — список голосов загрузится сам.\n" +
				"3. **Копирование голоса**: отметь в списке справа несколько реплик одного персонажа (лучше 1–3 минуты чистой речи) " +
				"и нажми «Скопировать голос из отмеченных». Копирование доступно на платных планах ElevenLabs."},
		{ID: "yandex", Title: "Яндекс SpeechKit", Tier: translate.Paid, NeedsKey: true,
			Note: "Пожалуй, лучшие русские голоса, с интонацией: радостно, дружелюбно, шёпотом. Оплата за объём текста, удобно платить из России.",
			Site: "https://console.yandex.cloud",
			Help: "1. Войди в **console.yandex.cloud** с Яндекс ID и создай платёжный аккаунт (новым аккаунтам дают стартовый грант).\n" +
				"2. В каталоге создай **сервисный аккаунт** с ролью **ai.speechkit-tts.user**.\n" +
				"3. Открой сервисный аккаунт → **Создать новый ключ → API-ключ**, область действия — **yc.ai.speechkitTts.execute**. Скопируй ключ сюда.\n\n" +
				"Голос с пометкой — с интонацией: «Джейн, радостно», «Марина, дружелюбно». Работает только для русского."},
		{ID: "salute", Title: "SaluteSpeech (Сбер)", Tier: translate.Paid, NeedsKey: true,
			Note: "Русские голоса Сбера: Наталья, Борис, Марфа, Тарас, Александра, Сергей. С 15 июля 2026 года Сбер не подключает новых клиентов.",
			Site: "https://developers.sber.ru/studio",
			Help: "**Подключиться заново нельзя:** с 15 июля 2026 года Сбер не принимает новых клиентов SaluteSpeech. " +
				"Если проект у тебя уже есть — он работает до конца оплаченного пакета.\n\n" +
				"1. Открой проект SaluteSpeech в **developers.sber.ru/studio** → **Настройки API**.\n" +
				"2. Скопируй **Authorization key** (ключ авторизации) сюда.\n\n" +
				"Серверы Сбера подписаны российским сертификатом НУЦ Минцифры: программа сама скачает его с gu-st.ru и будет доверять ему только в запросах к SaluteSpeech."},
		{ID: "gemini", Title: "Gemini (Google AI Studio)", Tier: translate.Paid, NeedsKey: true, Style: true,
			Note: "Голос понимает, как говорить: впиши словами, например «бодро, как тренер в фитнес-игре». 30 голосов, все говорят по-русски.",
			Site: "https://aistudio.google.com/apikey",
			Help: "1. Войди в **aistudio.google.com** с аккаунтом Google.\n" +
				"2. **Get API key → Create API key**, скопируй ключ сюда.\n" +
				"3. В поле **«Как говорить»** опиши подачу — она действует на все фразы, сам текст читается дословно.\n\n" +
				"Модель — " + geminiModel + ". Цены и бесплатный лимит — на странице тарифов Gemini API."},
		{ID: "google", Title: "Google Cloud Text-to-Speech", Tier: translate.Paid, NeedsKey: true,
			Note: "Голоса Google: для русского — Chirp 3 HD, WaveNet и обычные. Стабильно и недорого.",
			Site: "https://console.cloud.google.com/apis/library/texttospeech.googleapis.com",
			Help: "1. В **console.cloud.google.com** создай проект и подключи оплату.\n" +
				"2. Включи **Cloud Text-to-Speech API** (кнопка «Сайт» ниже).\n" +
				"3. **APIs & Services → Credentials → Create credentials → API key**; лучше ограничить ключ только Text-to-Speech. Скопируй ключ сюда.\n\n" +
				"Самые живые русские голоса — **Chirp3-HD** (Kore, Aoede, Leda, Zephyr — женские; Charon, Fenrir, Orus, Puck — мужские)."},
		{ID: "cartesia", Title: "Cartesia", Tier: translate.Paid, NeedsKey: true, Clone: true,
			Note: "Быстрые живые голоса и копирование голоса персонажа по короткому образцу. Альтернатива ElevenLabs.",
			Site: "https://play.cartesia.ai/keys",
			Help: "1. Зарегистрируйся на **play.cartesia.ai**.\n" +
				"2. **API Keys → New**, скопируй ключ сюда — список русских голосов загрузится сам.\n" +
				"3. **Копирование голоса**: отметь справа реплики одного персонажа и нажми «Скопировать голос из отмеченных» — " +
				"программа склеит до минуты речи и отправит образцом."},
	}
}

func NewRecognizer(id string, cfg Config) (Recognizer, error) {
	switch id {
	case "whispercpp":
		model := WhisperModelPath(cfg.Model)
		if _, err := os.Stat(model); err != nil {
			return nil, fmt.Errorf("модель Whisper «%s» не скачана — нажми «Скачать модель»", cfg.Model)
		}
		cfg.Model = model
		return &whisperLocal{cfg: cfg}, nil
	case "openai-stt":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("OpenAI: нужен API-ключ")
		}
		return &openAISTT{cfg}, nil
	}
	return nil, fmt.Errorf("неизвестное распознавание %q", id)
}

func NewSynthesizer(id string, cfg Config) (Synthesizer, error) {
	switch id {
	case "none", "":
		return nil, nil
	case "azure":
		if cfg.APIKey == "" || strings.TrimSpace(cfg.Region) == "" {
			return nil, fmt.Errorf("Azure: нужны ключ и регион (например, westeurope)")
		}
		return &azureTTS{cfg}, nil
	case "windows":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("голоса Windows есть только в Windows")
		}
		return &windowsTTS{cfg}, nil
	case "say":
		if _, err := exec.LookPath("say"); err != nil {
			return nil, fmt.Errorf("say есть только в macOS")
		}
		return &macSay{cfg}, nil
	case "openai-tts":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("OpenAI: нужен API-ключ")
		}
		return &openAITTS{cfg}, nil
	case "elevenlabs":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ElevenLabs: нужен API-ключ")
		}
		return &elevenLabs{cfg}, nil
	}
	return newLocalOrCloud(id, cfg)
}

// DecodeToWav переводит игровой звук в WAV. Форматы Nintendo (BFSTM, BFWAV, BWAV)
// читаются своим кодом, остальные (WEM, HCA, FSB, Opus…) — через vgmstream.
func DecodeToWav(ctx context.Context, in, out string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	if audio.Supported(b) {
		p, err := audio.Decode(b)
		if err != nil {
			return err
		}
		return os.WriteFile(out, audio.WAV(p), 0o644)
	}
	// встроенный ffmpeg читает многое не от Nintendo: Ogg, Opus, MP3, FLAC, ADX, HCA…
	if ffmpeg.Available() {
		if cmd, err := ffmpeg.Command("-loglevel", "error", "-y", "-i", in, "-c:a", "pcm_s16le", out); err == nil {
			if cmd.Run() == nil {
				return nil
			}
			os.Remove(out)
		}
	}
	bin, err := vgmstream(ctx)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "-o", out, in)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vgmstream: %v: %s", err, lastLine(b))
	}
	return nil
}

// FFmpegFormat — читает ли этот формат встроенный ffmpeg (если он вшит в сборку).
func FFmpegFormat(format string) bool {
	if !ffmpeg.Available() {
		return false
	}
	f := strings.ToUpper(format)
	for _, n := range []string{"OGG", "FLAC", "MP3", "WAV", "CRI HCA (", "ADX"} {
		if strings.HasPrefix(f, n) {
			return true
		}
	}
	return false
}

// NativeFormat — читает ли программа этот формат сама (по названию из отчёта: «BFSTM», «BWAV»…).
func NativeFormat(format string) bool {
	f := strings.ToUpper(format)
	for _, n := range []string{"BFSTM", "BFWAV", "BWAV", "BCSTM", "BCWAV", "BARS", "НАЧАЛО МУЗЫКИ"} {
		if strings.HasPrefix(f, n) {
			return true
		}
	}
	return false
}

// HaveVgmstream — найден ли vgmstream (в системе или скачанный программой).
func HaveVgmstream() bool {
	_, err := findTool("vgmstream-cli")
	if err != nil {
		_, err = os.Stat(filepath.Join(ToolsDir(), "vgmstream", vgmstreamExe()))
	}
	return err == nil
}

// whisperLocal — whisper.cpp, собранный внутрь программы.
type whisperLocal struct{ cfg Config }

var (
	whisperMu      sync.Mutex
	whisperModel   *whisper.Model
	whisperPath    string
	whisperAligned bool
)

// loadWhisper держит в памяти одну модель: загрузка large занимает секунды, а две большие модели
// разом занимают лишнюю память. aligned — с выравниванием слов (для видео), без него — быстрее (для звука).
func loadWhisper(path string, aligned bool) (*whisper.Model, error) {
	whisperMu.Lock()
	defer whisperMu.Unlock()
	if whisperModel != nil && whisperPath == path && whisperAligned == aligned {
		return whisperModel, nil
	}
	if whisperModel != nil {
		whisperModel.Close()
		whisperModel = nil
	}
	load := whisper.Load
	if aligned {
		load = whisper.LoadAligned
	}
	m, err := load(path)
	if err != nil {
		return nil, err
	}
	whisperModel, whisperPath, whisperAligned = m, path, aligned
	return m, nil
}

func (w *whisperLocal) Transcribe(ctx context.Context, wav string) (string, error) {
	b, err := os.ReadFile(wav)
	if err != nil {
		return "", err
	}
	p, err := audio.ReadWAV(b)
	if err != nil {
		return "", err
	}
	m, err := loadWhisper(w.cfg.Model, false)
	if err != nil {
		return "", err
	}
	lang := w.cfg.Lang
	if lang == "" {
		lang = "auto"
	}
	samples := audio.Mono16k(p)
	stop := func() bool { return ctx.Err() != nil }
	// длинный файл — чаще всего музыка: сначала слушаем первые 30 секунд, и если речи нет, дальше не идём
	const probe = 30 * whisper.SampleRate
	if len(samples) > probe*3/2 {
		text, err := m.Transcribe(samples[:probe], lang, stop)
		if err != nil || text == "" {
			return "", err
		}
	}
	return m.Transcribe(samples, lang, stop)
}

func (w *whisperLocal) Segments(ctx context.Context, wav string) ([]Segment, error) {
	b, err := os.ReadFile(wav)
	if err != nil {
		return nil, err
	}
	p, err := audio.ReadWAV(b)
	if err != nil {
		return nil, err
	}
	m, err := loadWhisper(w.cfg.Model, true)
	if err != nil {
		return nil, err
	}
	lang := w.cfg.Lang
	if lang == "" {
		lang = "auto"
	}
	segs, err := m.Segments(audio.Mono16k(p), lang, func() bool { return ctx.Err() != nil })
	out := make([]Segment, len(segs))
	for i, s := range segs {
		out[i] = Segment{Start: s.Start, End: s.End, Text: strings.TrimSpace(s.Text)}
	}
	return out, err
}

type openAISTT struct{ cfg Config }

func (o *openAISTT) Transcribe(ctx context.Context, wav string) (string, error) {
	f, err := os.Open(wav)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	model := o.cfg.Model
	if model == "" {
		model = "gpt-4o-transcribe"
	}
	mw.WriteField("model", model)
	if o.cfg.Lang != "" {
		mw.WriteField("language", o.cfg.Lang)
	}
	fw, _ := mw.CreateFormFile("file", filepath.Base(wav))
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &body)
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	data, err := do(req)
	if err != nil {
		return "", err
	}
	var resp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}

type macSay struct{ cfg Config }

func (m *macSay) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	out := outBase + ".wav"
	args := []string{"-o", out, "--file-format=WAVE", "--data-format=LEI16@22050"}
	if v := VoiceID(m.cfg.Model); v != "" {
		args = append(args, "-v", v)
	}
	args = append(args, "--", text)
	// say иногда зависает: файл записан, а процесс не выходит и заголовок WAV не дописан.
	// Ждём разумное время, потом обрываем и пробуем ещё раз.
	timeout := sayTimeout + time.Duration(len([]rune(text)))*100*time.Millisecond
	for range 2 {
		err := runSay(ctx, args, timeout)
		if err == nil {
			return out, nil
		}
		os.Remove(out)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !errors.Is(err, errSayHung) {
			return "", err
		}
	}
	return "", fmt.Errorf("синтезатор macOS дважды завис на этой фразе — пропускаю; её можно озвучить заново в правке звука или записать своим голосом")
}

var (
	errSayHung = errors.New("say завис")
	sayTimeout = 20 * time.Second // плюс 0,1 с на каждый символ фразы
)

func runSay(ctx context.Context, args []string, timeout time.Duration) error {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "say", args...)
	cmd.WaitDelay = 2 * time.Second
	b, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && tctx.Err() != nil {
		return errSayHung
	}
	return fmt.Errorf("say: %v: %s", err, lastLine(b))
}

type openAITTS struct{ cfg Config }

func (o *openAITTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	model, voice := "gpt-4o-mini-tts", VoiceID(o.cfg.Model)
	if m, v, ok := strings.Cut(voice, ":"); ok {
		model, voice = m, v
	}
	if voice == "" {
		voice = "alloy"
	}
	b, _ := json.Marshal(map[string]string{"model": model, "voice": voice, "input": text, "response_format": "wav"})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	data, err := do(req)
	if err != nil {
		return "", err
	}
	out := outBase + ".wav"
	return out, os.WriteFile(out, data, 0o644)
}

type elevenLabs struct{ cfg Config }

func (e *elevenLabs) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	b, _ := json.Marshal(map[string]string{"text": text, "model_id": "eleven_multilingual_v2"})
	// сырой PCM, чтобы озвучку можно было записать обратно в игру
	u := "https://api.elevenlabs.io/v1/text-to-speech/" + VoiceID(e.cfg.Model) + "?output_format=pcm_24000"
	req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	req.Header.Set("xi-api-key", e.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	data, err := do(req)
	if err != nil {
		return "", err
	}
	pcm := &audio.PCM{Rate: 24000, Channels: [][]int16{make([]int16, len(data)/2)}}
	for i := range pcm.Channels[0] {
		pcm.Channels[0][i] = int16(uint16(data[i*2]) | uint16(data[i*2+1])<<8)
	}
	out := outBase + ".wav"
	return out, os.WriteFile(out, audio.WAV(pcm), 0o644)
}

var client = &http.Client{Timeout: 5 * time.Minute}

func do(req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, data)
	}
	return data, nil
}

// findTool ищет утилиту в PATH и в стандартных папках Homebrew
// (у приложения, запущенного из Finder, PATH урезанный).
func findTool(names ...string) (string, error) {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("не найдено: %s", strings.Join(names, ", "))
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
