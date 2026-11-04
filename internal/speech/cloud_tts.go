package speech

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"switch-translator/internal/audio"
)

// Облачные голоса с ключом: Яндекс SpeechKit, SaluteSpeech (Сбер), Gemini, Google Cloud, Cartesia.
// Все отдают 16-битный звук — его можно вписать обратно в игру.

// writePCM16 сохраняет сырой 16-битный моно PCM как WAV.
func writePCM16(out string, data []byte, rate int) error {
	p := &audio.PCM{Rate: rate, Channels: [][]int16{make([]int16, len(data)/2)}}
	for i := range p.Channels[0] {
		p.Channels[0][i] = int16(uint16(data[i*2]) | uint16(data[i*2+1])<<8)
	}
	return os.WriteFile(out, audio.WAV(p), 0o644)
}

// writeAudio сохраняет ответ сервиса: готовый WAV — как есть, сырой PCM — с заголовком.
func writeAudio(out string, data []byte, rate int) error {
	if bytes.HasPrefix(data, []byte("RIFF")) {
		return os.WriteFile(out, data, 0o644)
	}
	return writePCM16(out, data, rate)
}

// locale — язык в виде «ru-RU».
func locale(lang string) string {
	if strings.Contains(lang, "-") {
		return lang
	}
	if l, ok := map[string]string{"ru": "ru-RU", "en": "en-US", "uk": "uk-UA", "ja": "ja-JP", "de": "de-DE", "fr": "fr-FR",
		"es": "es-ES", "it": "it-IT", "pt": "pt-BR", "pl": "pl-PL", "zh": "cmn-CN", "ko": "ko-KR", "kk": "kk-KZ"}[lang]; ok {
		return l
	}
	if lang == "" {
		return "ru-RU"
	}
	return lang + "-" + strings.ToUpper(lang)
}

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------- Яндекс SpeechKit (API v1) ----------

// yandexVoices — голоса, доступные в API v1, с ролями: «jane:good» — Джейн радостно.
var yandexVoices = []struct{ ID, Name string }{
	{"marina", "Марина, женский"}, {"marina:friendly", "Марина, женский, дружелюбно"}, {"marina:whisper", "Марина, женский, шёпотом"},
	{"jane", "Джейн, женский"}, {"jane:good", "Джейн, женский, радостно"}, {"jane:evil", "Джейн, женский, раздражённо"},
	{"omazh", "Омаж, женский"}, {"omazh:evil", "Омаж, женский, раздражённо"},
	{"filipp", "Филипп, мужской"},
	{"ermil", "Ермил, мужской"}, {"ermil:good", "Ермил, мужской, радостно"},
	{"zahar", "Захар, мужской"}, {"zahar:good", "Захар, мужской, радостно"},
	{"madi_ru", "Мади, мужской"},
}

type yandexTTS struct{ cfg Config }

func (y *yandexTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(y.cfg.Model)
	if voice == "" {
		voice = "marina"
	}
	f := url.Values{"text": {text}, "lang": {"ru-RU"}, "format": {"lpcm"}, "sampleRateHertz": {"48000"}}
	if v, role, ok := strings.Cut(voice, ":"); ok {
		f.Set("voice", v)
		f.Set("emotion", role)
	} else {
		f.Set("voice", voice)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://tts.api.cloud.yandex.net/speech/v1/tts:synthesize", strings.NewReader(f.Encode()))
	req.Header.Set("Authorization", "Api-Key "+y.cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	data, err := do(req)
	if err != nil {
		return "", fmt.Errorf("Яндекс SpeechKit: %w", err)
	}
	out := outBase + ".wav"
	return out, writePCM16(out, data, 48000)
}

// ---------- SaluteSpeech (Сбер) ----------

// saluteVoices — голоса SaluteSpeech (24 кГц).
var saluteVoices = []struct{ ID, Name string }{
	{"Nec_24000", "Наталья, женский"}, {"May_24000", "Марфа, женский"}, {"Ost_24000", "Александра, женский"},
	{"Bys_24000", "Борис, мужской"}, {"Tur_24000", "Тарас, мужской"}, {"Pon_24000", "Сергей, мужской"},
	{"Kin_24000", "Kira, английский"},
}

// Серверы Сбера подписаны российским корневым сертификатом (НУЦ Минцифры), которого нет в системах
// по умолчанию. Программа один раз скачивает его с официального адреса и доверяет ему только
// в запросах к SaluteSpeech.
const russianRootURL = "https://gu-st.ru/content/Other/doc/russian_trusted_root_ca.cer"

var (
	saluteMu     sync.Mutex
	saluteClient *http.Client
	saluteToken  string
	saluteKey    string
	saluteExpiry time.Time
)

func saluteHTTP(ctx context.Context) (*http.Client, error) {
	if saluteClient != nil {
		return saluteClient, nil
	}
	path := filepath.Join(ToolsDir(), "russian_trusted_root_ca.cer")
	der, err := os.ReadFile(path)
	if err != nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", russianRootURL, nil)
		if der, err = do(req); err != nil {
			return nil, fmt.Errorf("SaluteSpeech: не удалось скачать сертификат НУЦ Минцифры: %w", err)
		}
	}
	if b, _ := pem.Decode(der); b != nil {
		der = b.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("SaluteSpeech: сертификат НУЦ Минцифры не читается: %w", err)
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, der, 0o644)
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AddCert(cert)
	saluteClient = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	return saluteClient, nil
}

// saluteAuth — токен доступа (живёт 30 минут, берётся заново за минуту до конца).
func saluteAuth(ctx context.Context, key string) (*http.Client, string, error) {
	saluteMu.Lock()
	defer saluteMu.Unlock()
	c, err := saluteHTTP(ctx)
	if err != nil {
		return nil, "", err
	}
	if saluteToken != "" && saluteKey == key && time.Until(saluteExpiry) > time.Minute {
		return c, saluteToken, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://ngw.devices.sberbank.ru:9443/api/v2/oauth",
		strings.NewReader("scope=SALUTE_SPEECH_PERS"))
	req.Header.Set("Authorization", "Basic "+key)
	req.Header.Set("RqUID", uuid4())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	data, err := doWith(c, req)
	if err != nil {
		return nil, "", fmt.Errorf("SaluteSpeech, вход: %w", err)
	}
	var resp struct {
		Token   string `json:"access_token"`
		Expires int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Token == "" {
		return nil, "", fmt.Errorf("SaluteSpeech не выдал токен: %.200s", data)
	}
	exp := time.Unix(resp.Expires, 0)
	if resp.Expires > 1e12 { // в документации встречаются и секунды, и миллисекунды
		exp = time.UnixMilli(resp.Expires)
	}
	saluteToken, saluteKey, saluteExpiry = resp.Token, key, exp
	return c, saluteToken, nil
}

type saluteTTS struct{ cfg Config }

func (s *saluteTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	c, token, err := saluteAuth(ctx, s.cfg.APIKey)
	if err != nil {
		return "", err
	}
	voice := VoiceID(s.cfg.Model)
	if voice == "" {
		voice = "Nec_24000"
	}
	u := "https://smartspeech.sber.ru/rest/v1/text:synthesize?format=pcm16&voice=" + url.QueryEscape(voice)
	req, _ := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(text))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/text")
	data, err := doWith(c, req)
	if err != nil {
		return "", fmt.Errorf("SaluteSpeech: %w", err)
	}
	rate := 24000
	if strings.HasSuffix(voice, "_8000") {
		rate = 8000
	}
	out := outBase + ".wav"
	return out, writePCM16(out, data, rate)
}

// ---------- Gemini (Google AI Studio) ----------

const geminiModel = "gemini-3.8-flash-tts"

// geminiVoices — готовые голоса Gemini; по-русски говорят все.
var geminiVoices = []struct{ ID, Name string }{
	{"Kore", "женский"}, {"Aoede", "женский"}, {"Leda", "женский"}, {"Zephyr", "женский"}, {"Autonoe", "женский"},
	{"Callirrhoe", "женский"}, {"Despina", "женский"}, {"Erinome", "женский"}, {"Gacrux", "женский"}, {"Laomedeia", "женский"},
	{"Pulcherrima", "женский"}, {"Sulafat", "женский"}, {"Vindemiatrix", "женский"}, {"Achernar", "женский"},
	{"Puck", "мужской"}, {"Charon", "мужской"}, {"Fenrir", "мужской"}, {"Orus", "мужской"}, {"Enceladus", "мужской"},
	{"Iapetus", "мужской"}, {"Umbriel", "мужской"}, {"Algieba", "мужской"}, {"Algenib", "мужской"}, {"Rasalgethi", "мужской"},
	{"Alnilam", "мужской"}, {"Schedar", "мужской"}, {"Achird", "мужской"}, {"Zubenelgenubi", "мужской"},
	{"Sadachbia", "мужской"}, {"Sadaltager", "мужской"},
}

type geminiTTS struct{ cfg Config }

func (g *geminiTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(g.cfg.Model)
	if voice == "" {
		voice = "Kore"
	}
	part := map[string]any{"text": text}
	if g.cfg.Style != "" {
		part["speech_metadata"] = map[string]string{"style": g.cfg.Style} // как говорить — отдельно от текста: текст читается дословно
	}
	body := func(legacy bool) map[string]any {
		gen := map[string]any{"responseModalities": []string{"AUDIO"}}
		if legacy { // поле голоса в старом виде — на случай, если новое модель не примет
			gen["speechConfig"] = map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": voice}}}
		} else {
			gen["speechConfig"] = map[string]any{"voiceConfig": map[string]string{"voice": voice}}
			gen["responseFormat"] = map[string]any{"audio": map[string]any{"mimeType": "AUDIO_L16", "sampleRate": 24000}}
		}
		return map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{part}}}, "generationConfig": gen}
	}
	var data []byte
	var err error
	for _, legacy := range []bool{false, true} {
		b, _ := json.Marshal(body(legacy))
		req, _ := http.NewRequestWithContext(ctx, "POST",
			"https://generativelanguage.googleapis.com/v1beta/models/"+geminiModel+":generateContent", bytes.NewReader(b))
		req.Header.Set("x-goog-api-key", g.cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		if data, err = do(req); err == nil || !strings.HasPrefix(err.Error(), "HTTP 400") {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("Gemini: %w", err)
	}
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("Gemini не вернул звук: %.200s", data)
	}
	pcm, err := base64.StdEncoding.DecodeString(resp.Candidates[0].Content.Parts[0].InlineData.Data)
	if err != nil || len(pcm) == 0 {
		return "", fmt.Errorf("Gemini не вернул звук")
	}
	out := outBase + ".wav"
	return out, writeAudio(out, pcm, 24000)
}

// ---------- Google Cloud Text-to-Speech ----------

type googleTTS struct{ cfg Config }

func googleDefault(lang string) string { return locale(lang) + "-Chirp3-HD-Kore" }

func (g *googleTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(g.cfg.Model)
	if voice == "" {
		voice = googleDefault(g.cfg.Lang)
	}
	parts := strings.SplitN(voice, "-", 3)
	if len(parts) < 3 {
		return "", fmt.Errorf("Google Cloud: голос «%s» не похож на ru-RU-…", voice)
	}
	b, _ := json.Marshal(map[string]any{
		"input":       map[string]string{"text": text},
		"voice":       map[string]string{"languageCode": parts[0] + "-" + parts[1], "name": voice},
		"audioConfig": map[string]any{"audioEncoding": "LINEAR16", "sampleRateHertz": 24000},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://texttospeech.googleapis.com/v1/text:synthesize", bytes.NewReader(b))
	req.Header.Set("x-goog-api-key", g.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	data, err := do(req)
	if err != nil {
		return "", fmt.Errorf("Google Cloud: %w", err)
	}
	var resp struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	wav, err := base64.StdEncoding.DecodeString(resp.AudioContent)
	if err != nil || len(wav) == 0 {
		return "", fmt.Errorf("Google Cloud не вернул звук")
	}
	out := outBase + ".wav"
	return out, writeAudio(out, wav, 24000) // LINEAR16 приходит уже с заголовком WAV
}

// googleVoices — голоса Google Cloud для языка.
func googleVoices(ctx context.Context, key, lang string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://texttospeech.googleapis.com/v1/voices?languageCode="+locale(lang), nil)
	req.Header.Set("x-goog-api-key", key)
	data, err := do(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Voices []struct {
			Name       string `json:"name"`
			SsmlGender string `json:"ssmlGender"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range resp.Voices {
		g := map[string]string{"FEMALE": "женский", "MALE": "мужской"}[v.SsmlGender]
		out = append(out, VoiceLabel(g, v.Name))
	}
	return out, nil
}

// ---------- Cartesia ----------

const (
	cartesiaVersion = "2026-08-14"
	cartesiaModel   = "sonic-3.6"
)

func cartesiaReq(ctx context.Context, key, method, path string, body io.Reader) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, method, "https://api.cartesia.ai"+path, body)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Cartesia-Version", cartesiaVersion)
	return req
}

type cartesiaTTS struct{ cfg Config }

func (c *cartesiaTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(c.cfg.Model)
	if voice == "" {
		return "", fmt.Errorf("Cartesia: выбери голос")
	}
	lang := c.cfg.Lang
	if lang == "" {
		lang = "ru"
	}
	b, _ := json.Marshal(map[string]any{
		"model_id": cartesiaModel, "transcript": text, "voice": voice, "language": lang,
		"output_format": map[string]any{"container": "wav", "encoding": "pcm_s16le", "sample_rate": 24000},
	})
	req := cartesiaReq(ctx, c.cfg.APIKey, "POST", "/tts/bytes", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	data, err := do(req)
	if err != nil {
		return "", fmt.Errorf("Cartesia: %w", err)
	}
	out := outBase + ".wav"
	return out, writeAudio(out, data, 24000)
}

// cartesiaVoices — голоса Cartesia для языка (свои скопированные — тоже).
func cartesiaVoices(ctx context.Context, key, lang string) ([]string, error) {
	if lang == "" {
		lang = "ru"
	}
	data, err := do(cartesiaReq(ctx, key, "GET", "/voices?limit=100&language="+url.QueryEscape(lang), nil))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID, Name, Gender string
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range resp.Data {
		name := v.Name
		if g := map[string]string{"feminine": "женский", "masculine": "мужской"}[v.Gender]; g != "" {
			name += ", " + g
		}
		out = append(out, VoiceLabel(name, v.ID))
	}
	return out, nil
}

// cloneCartesia создаёт голос по образцам: Cartesia берёт один файл, поэтому образцы склеиваются
// (не длиннее минуты — этого хватает с запасом).
func cloneCartesia(ctx context.Context, key, lang, name string, samples []string) (string, error) {
	joined := &audio.PCM{Rate: 24000, Channels: [][]int16{nil}}
	for _, s := range samples {
		b, err := os.ReadFile(s)
		if err != nil {
			return "", err
		}
		p, err := audio.ReadWAV(b)
		if err != nil {
			return "", err
		}
		p = audio.Resample(p, 24000)
		joined.Channels[0] = append(joined.Channels[0], p.Channels[0]...)
		joined.Channels[0] = append(joined.Channels[0], make([]int16, 24000/4)...) // четверть секунды тишины между репликами
		if joined.Seconds() >= 60 {
			break
		}
	}
	if lang == "" {
		lang = "ru"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("name", name)
	mw.WriteField("language", lang)
	fw, _ := mw.CreateFormFile("clip", "voice.wav")
	fw.Write(audio.WAV(joined))
	mw.Close()
	req := cartesiaReq(ctx, key, "POST", "/voices/clone", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	data, err := do(req)
	if err != nil {
		return "", fmt.Errorf("Cartesia: %w", err)
	}
	var resp struct{ ID string }
	if err := json.Unmarshal(data, &resp); err != nil || resp.ID == "" {
		return "", fmt.Errorf("Cartesia не вернул голос: %.200s", data)
	}
	return VoiceLabel(name, resp.ID), nil
}

// doWith — do, но через свой клиент (SaluteSpeech со своим сертификатом).
func doWith(c *http.Client, req *http.Request) ([]byte, error) {
	resp, err := c.Do(req)
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
