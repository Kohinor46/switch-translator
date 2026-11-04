// Package translate — переводчики текста: бесплатные API, платные API и LLM (ChatGPT, Claude).
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"switch-translator/internal/i18n"
	"time"
	"unicode"
)

type Tier int

const (
	Free         Tier = iota // без ключа
	FreeTier                 // бесплатно с ограничениями, нужен ключ или свой сервер
	Paid                     // оплата по API
	Subscription             // через установленную программу и её подписку
)

func (t Tier) Title() string {
	return [...]string{"бесплатно", "бесплатный лимит", "платно", "по подписке"}[t]
}

// Option — переводчик в списке выбора вместе с инструкцией, как его подключить.
type Option struct {
	ID       string
	Title    string
	Tier     Tier
	KeyLabel string // подпись поля ключа; "" — поля нет
	NeedsKey bool   // ключ обязателен
	NeedsURL bool
	URL      string // значение по умолчанию
	Models   []string
	Note     string   // одна строка под выбором
	Site     string   // куда идти за ключом или программой
	Help     string   // как подключить: шаги в markdown
	Commands []string // команды, которые можно скопировать
	LLM      bool     // понимает контекст и глоссарий
}

func (o Option) Label() string {
	return fmt.Sprintf("%s — %s", i18n.T(o.Title), i18n.T(o.Tier.Title()))
}

type Config struct {
	APIKey  string
	URL     string
	Model   string
	From    string // код языка: ja, en, …
	To      string
	Context string // описание игры и глоссарий для LLM
	// Glossary — словарь игры; ИИ-переводчики получают с каждой пачкой статьи, что в ней встречаются
	Glossary []Term
}

type Translator interface {
	Translate(ctx context.Context, texts []string) ([]string, error)
	BatchSize() int
}

// Concurrent — переводчик, которому можно отдавать несколько пачек одновременно.
type Concurrent interface {
	Workers() int
}

// Workers — сколько пачек переводить одновременно.
func Workers(t Translator) int {
	if c, ok := t.(Concurrent); ok {
		return max(c.Workers(), 1)
	}
	return 1
}

func Options() []Option {
	return []Option{
		{ID: "mymemory", Title: "MyMemory", Tier: Free, KeyLabel: "E-mail",
			Note: "Перевод выполняется на сайте MyMemory (Translated, Италия). Регистрация не нужна.",
			Site: "https://mymemory.translated.net",
			Help: "Без e-mail — около **5000 символов в день**: хватит попробовать.\n\n" +
				"Если вписать свой e-mail — лимит вырастет до **50 000 символов в день**. Регистрироваться не нужно: " +
				"e-mail просто передаётся вместе с запросом, чтобы MyMemory знал, кто переводит."},
		{ID: "libre", Title: "LibreTranslate", Tier: FreeTier, KeyLabel: "API-ключ", NeedsURL: true, URL: "http://localhost:5000",
			Note: "Свой сервер на этом компьютере: бесплатно, без лимитов, текст никуда не уходит.",
			Site: "https://github.com/LibreTranslate/LibreTranslate",
			Help: "1. Установи **Docker Desktop** (docker.com) и запусти его.\n" +
				"2. Запусти сервер командой ниже. Первый запуск скачивает языковые модели — это несколько гигабайт и 5–15 минут.\n" +
				"3. Когда в браузере откроется http://localhost:5000 — можно переводить.\n\n" +
				"**Ключ на своём сервере не нужен** — оставь поле пустым. Ключ нужен только для публичного сервера libretranslate.com (он платный).",
			Commands: []string{
				"docker run -d -p 5000:5000 --name libretranslate libretranslate/libretranslate --load-only en,ja,ru,de,fr,es,it",
				"docker start libretranslate",
			}},
		{ID: "deepl", Title: "DeepL", Tier: FreeTier, KeyLabel: "API-ключ", NeedsKey: true,
			Note: "Один из лучших машинных переводчиков. 500 000 символов в месяц бесплатно.",
			Site: "https://www.deepl.com/pro-api",
			Help: "1. Открой сайт и выбери план **DeepL API Free** (не обычный DeepL Pro!). При регистрации попросят карту — только для проверки, денег не спишут.\n" +
				"2. В аккаунте открой **API Keys & Limits** и скопируй ключ.\n" +
				"3. Ключ бесплатного плана заканчивается на **:fx** — программа сама поймёт, что он бесплатный."},
		{ID: "google", Title: "Google Cloud Translation", Tier: Paid, KeyLabel: "API-ключ", NeedsKey: true,
			Note: "Переводчик Google через Google Cloud. Нужен аккаунт с привязанной картой.",
			Site: "https://console.cloud.google.com/apis/library/translate.googleapis.com",
			Help: "1. Войди в **Google Cloud Console** и создай проект.\n" +
				"2. Подключи оплату (Billing) — без неё API не работает. У Google есть бесплатный месячный лимит — условия на сайте.\n" +
				"3. Открой ссылку ниже и нажми **Enable** у Cloud Translation API.\n" +
				"4. **APIs & Services → Credentials → Create credentials → API key**. Скопируй ключ и ограничь его только Cloud Translation API."},
		{ID: "openai", Title: "ChatGPT (OpenAI API)", Tier: Paid, KeyLabel: "API-ключ", NeedsKey: true, LLM: true,
			Models: []string{"gpt-4.1-mini", "gpt-4.1", "gpt-4o-mini"},
			Note:   "Учитывает контекст и глоссарий. Оплата за объём текста.",
			Site:   "https://platform.openai.com/api-keys",
			Help: "1. Войди на **platform.openai.com** (тот же аккаунт, что в ChatGPT).\n" +
				"2. **Settings → Billing**: пополни баланс — API оплачивается отдельно от подписки ChatGPT Plus.\n" +
				"3. **API keys → Create new secret key**, скопируй ключ (он показывается один раз).\n\n" +
				"Есть подписка ChatGPT и не хочется платить за API — выбери «**Codex**»: он переводит в счёт подписки."},
		{ID: "codex", Title: "Codex (подписка ChatGPT)", Tier: Subscription, LLM: true,
			Note: "Переводит установленный Codex — в счёт твоей подписки ChatGPT, без API-ключа.",
			Site: "https://developers.openai.com/codex",
			Help: "Codex входит в приложение **ChatGPT** для Mac — программа найдёт его сама. Можно поставить и отдельно, командой ниже.\n\n" +
				"Один раз войди в свой аккаунт ChatGPT: запусти в Терминале `codex login`.\n\n" +
				"Каждая пачка строк — отдельный запуск Codex, поэтому это медленнее, чем API.",
			Commands: []string{"npm install -g @openai/codex", "codex login"}},
		{ID: "claude", Title: "Claude (Anthropic API)", Tier: Paid, KeyLabel: "API-ключ", NeedsKey: true, LLM: true,
			Models: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"},
			Note:   "Учитывает контекст и глоссарий. Opus — лучшее качество, Haiku — дешевле всего.",
			Site:   "https://console.anthropic.com/settings/keys",
			Help: "1. Войди в **Claude Console** (console.anthropic.com).\n" +
				"2. **Billing**: пополни баланс — API оплачивается отдельно от подписки Claude Pro/Max.\n" +
				"3. **API Keys → Create Key**, скопируй ключ.\n\n" +
				"Есть подписка Claude — выбери «**Claude Code**»: он переводит в счёт подписки, без ключа."},
		{ID: "claudecode", Title: "Claude Code (подписка Claude)", Tier: Subscription, LLM: true,
			Models: []string{"", "opus", "sonnet", "haiku"},
			Note:   "Переводит установленный Claude Code — в счёт твоей подписки Claude, без API-ключа.",
			Site:   "https://code.claude.com",
			Help: "Claude Code встроен в приложение **Claude** для Mac — программа найдёт его сама. Можно поставить и отдельно, командой ниже.\n\n" +
				"Один раз войди в аккаунт: запусти в Терминале `claude` и пройди вход.\n\n" +
				"Модель можно не выбирать — возьмётся та, что стоит у тебя по умолчанию.",
			Commands: []string{"curl -fsSL https://claude.ai/install.sh | bash", "claude"}},
	}
}

func FindOption(id string) (Option, bool) {
	for _, o := range Options() {
		if o.ID == id {
			return o, true
		}
	}
	return Option{}, false
}

func New(id string, cfg Config) (Translator, error) {
	opt, ok := FindOption(id)
	if !ok {
		return nil, fmt.Errorf("неизвестный переводчик %q", id)
	}
	if opt.NeedsKey && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("%s: нужен API-ключ", opt.Title)
	}
	if cfg.Model == "" && len(opt.Models) > 0 && opt.Models[0] != "" {
		cfg.Model = opt.Models[0]
	}
	if cfg.URL == "" {
		cfg.URL = opt.URL
	}
	switch id {
	case "mymemory":
		return &myMemory{cfg}, nil
	case "libre":
		return &libre{cfg}, nil
	case "deepl":
		return &deepL{cfg}, nil
	case "google":
		return &google{cfg}, nil
	case "openai":
		return &llm{cfg: cfg, call: openAIChat, batch: 60, workers: 6}, nil
	case "claude":
		return newClaude(cfg), nil
	case "codex":
		bin, err := FindCodex()
		if err != nil {
			return nil, err
		}
		return &llm{cfg: cfg, call: codexCLI(bin), batch: 100, workers: 4}, nil
	case "claudecode":
		bin, err := FindClaudeCode()
		if err != nil {
			return nil, err
		}
		return &llm{cfg: cfg, call: claudeCLI(bin), batch: 100, workers: 4}, nil
	}
	return nil, fmt.Errorf("переводчик %q не реализован", id)
}

var Languages = []struct{ Code, Name, English string }{
	{"ja", "японский", "Japanese"},
	{"en", "английский", "English"},
	{"ru", "русский", "Russian"},
	{"uk", "украинский", "Ukrainian"},
	{"zh", "китайский", "Chinese (Simplified)"},
	{"ko", "корейский", "Korean"},
	{"de", "немецкий", "German"},
	{"fr", "французский", "French"},
	{"es", "испанский", "Spanish"},
	{"it", "итальянский", "Italian"},
	{"nl", "нидерландский", "Dutch"},
	{"pt", "португальский", "Portuguese"},
}

// LanguageName — название языка по-русски: "en" → "английский".
func LanguageName(code string) string {
	for _, l := range Languages {
		if l.Code == code {
			return l.Name
		}
	}
	return code
}

func englishName(code string) string {
	for _, l := range Languages {
		if l.Code == code {
			return l.English
		}
	}
	return code
}

// Теги игры {{...}} машинные переводчики ломают, поэтому на время перевода
// заменяем их на ⟦n⟧ — такие скобки они обычно не трогают.
var tagRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

func protect(s string) (string, []string) {
	var tags []string
	out := tagRe.ReplaceAllStringFunc(s, func(t string) string {
		tags = append(tags, t)
		return fmt.Sprintf("⟦%d⟧", len(tags)-1)
	})
	return out, tags
}

func restore(s string, tags []string) string {
	for i, t := range tags {
		s = strings.Replace(s, fmt.Sprintf("⟦%d⟧", i), t, 1)
	}
	return s
}

// mtBatch оборачивает машинный перевод защитой тегов. Строки без букв (только теги,
// цифры, знаки) не отправляются — экономит лимит и запросы.
func mtBatch(ctx context.Context, texts []string, fn func(context.Context, []string) ([]string, error)) ([]string, error) {
	res := append([]string(nil), texts...)
	var send []string
	var idx []int
	tags := make([][]string, len(texts))
	for i, t := range texts {
		p, tg := protect(t)
		if !hasLetters(tagRe.ReplaceAllString(t, "")) {
			continue
		}
		tags[i] = tg
		send = append(send, p)
		idx = append(idx, i)
	}
	if len(send) == 0 {
		return res, nil
	}
	out, err := fn(ctx, send)
	if err != nil {
		return nil, err
	}
	if len(out) != len(send) {
		return nil, fmt.Errorf("переводчик вернул %d строк вместо %d", len(out), len(send))
	}
	for j, i := range idx {
		res[i] = restore(out[j], tags[i])
	}
	return res, nil
}

func hasLetters(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// retryable — ошибка, после которой есть смысл подождать и повторить (лимит частоты, сбой сервера, сеть).
type retryable struct{ error }

func doJSON(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return withRetry(ctx, func() error {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return retryable{err}
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return retryable{err}
		}
		if resp.StatusCode/100 != 2 {
			msg := string(data)
			if len(msg) > 500 {
				msg = msg[:500]
			}
			err := fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				return retryable{err}
			}
			return err
		}
		return json.Unmarshal(data, out)
	})
}

// withRetry повторяет fn при retryable-ошибках: паузы 2, 4, 8, 16, 32 с.
func withRetry(ctx context.Context, fn func() error) error {
	delay := 2 * time.Second
	for attempt := 0; ; attempt++ {
		err := fn()
		var r retryable
		if !errors.As(err, &r) {
			return err
		}
		if attempt == 5 {
			return fmt.Errorf("после %d попыток: %w", attempt+1, r.error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}
