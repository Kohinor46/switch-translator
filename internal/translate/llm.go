package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// llm — общий перевод через языковую модель: пачка строк в JSON туда и обратно.
type llm struct {
	cfg     Config
	call    func(ctx context.Context, cfg Config, system, user string) (string, error)
	batch   int // строк за запрос: больше — меньше накладных расходов на каждый запуск
	workers int // запросов одновременно
}

func (l *llm) BatchSize() int {
	if l.batch > 0 {
		return l.batch
	}
	return 40
}

func (l *llm) Workers() int { return max(l.workers, 1) }

func systemPrompt(cfg Config) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a professional video game localizer. Translate in-game text from %s to %s.\n",
		englishName(cfg.From), englishName(cfg.To))
	sb.WriteString(`Rules:
- Translate naturally, the way a published localization would read; keep each character's tone.
- Tags like {{1}}, {{2}} are game control codes (button icons, colors, pauses, names): keep every tag exactly once, unchanged, next to the matching words.
- Keep line breaks (\n) roughly where they are: the text has to fit the same dialogue box.
- Keep names, terms and places consistent across all strings.
- Return only a JSON object {"translations": [...]} with exactly as many strings as the input, in the same order.
`)
	if c := strings.TrimSpace(cfg.Context); c != "" {
		sb.WriteString("\nAbout the game and glossary:\n")
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	return sb.String()
}

// Translate отправляет пачку модели. Длинные коды игры ({{10:1:1800530075…}}) на время перевода
// заменяются короткими {{1}}, {{2}}: модели не нужно переписывать шестнадцатеричные хвосты — ответ
// выходит заметно короче и быстрее, а коды не искажаются. Строки без букв (только коды и знаки) не отправляются.
func (l *llm) Translate(ctx context.Context, texts []string) ([]string, error) {
	res := append([]string(nil), texts...)
	tags := make([][]string, len(texts))
	var idx []int
	for i, t := range texts {
		if !hasLetters(tagRe.ReplaceAllString(t, "")) {
			continue
		}
		idx = append(idx, i)
	}
	for attempt := 0; attempt < 2 && len(idx) > 0; attempt++ {
		send := make([]string, len(idx))
		for j, i := range idx {
			send[j], tags[i] = shortTags(texts[i])
		}
		out, err := l.ask(ctx, send)
		if err != nil {
			return nil, err
		}
		var bad []int
		for j, i := range idx {
			if r, ok := restoreShort(out[j], tags[i]); ok {
				res[i] = r
			} else {
				bad = append(bad, i)
			}
		}
		idx = bad // строки с потерянными кодами — ещё раз, отдельной пачкой
	}
	for _, i := range idx {
		res[i] = "" // коды так и не сошлись: строка останется непереведённой, её переведут при следующем запуске
	}
	return res, nil
}

func (l *llm) ask(ctx context.Context, texts []string) ([]string, error) {
	in, _ := json.Marshal(texts)
	user := glossaryPrompt(glossaryFor(l.cfg.Glossary, texts), l.cfg.To) + fmt.Sprintf("Translate these %d strings:\n%s", len(texts), in)
	out, err := l.call(ctx, l.cfg, systemPrompt(l.cfg), user)
	if err != nil {
		return nil, err
	}
	return parseTranslations(out, len(texts))
}

var shortTagRe = regexp.MustCompile(`\{\{(\d+)\}\}`)

// shortTags: «{{10:1:18005300…}}Hi {{3:0:}}» → «{{1}}Hi {{2}}».
func shortTags(s string) (string, []string) {
	var tags []string
	out := tagRe.ReplaceAllStringFunc(s, func(t string) string {
		tags = append(tags, t)
		return fmt.Sprintf("{{%d}}", len(tags))
	})
	return out, tags
}

// restoreShort возвращает коды на место; false — модель потеряла, повторила или выдумала код.
func restoreShort(s string, tags []string) (string, bool) {
	seen := make([]int, len(tags))
	ok := true
	out := shortTagRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[2 : len(m)-2])
		if n < 1 || n > len(tags) {
			ok = false
			return m
		}
		seen[n-1]++
		return tags[n-1]
	})
	for _, c := range seen {
		if c != 1 {
			ok = false
		}
	}
	// остатки длинных кодов в ответе — значит модель вставила код сама
	if len(tagRe.FindAllString(out, -1)) != len(tags) {
		ok = false
	}
	return out, ok
}

func parseTranslations(s string, want int) ([]string, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("модель не вернула JSON: %.200s", s)
	}
	var resp struct {
		Translations []string `json:"translations"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &resp); err != nil {
		return nil, fmt.Errorf("не разобрал ответ модели: %w", err)
	}
	if len(resp.Translations) != want {
		return nil, fmt.Errorf("модель вернула %d строк вместо %d", len(resp.Translations), want)
	}
	return resp.Translations, nil
}

func openAIChat(ctx context.Context, cfg Config, system, user string) (string, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := doJSON(ctx, "POST", base+"/chat/completions", map[string]string{"Authorization": "Bearer " + cfg.APIKey}, body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("OpenAI вернул пустой ответ")
	}
	if r := resp.Choices[0].Message.Refusal; r != "" {
		return "", fmt.Errorf("OpenAI отказался: %s", r)
	}
	return resp.Choices[0].Message.Content, nil
}

func newClaude(cfg Config) *llm {
	client := anthropic.NewClient(option.WithAPIKey(cfg.APIKey))
	call := func(ctx context.Context, cfg Config, system, user string) (string, error) {
		params := anthropic.BetaMessageNewParams{
			Model:        anthropic.Model(cfg.Model),
			MaxTokens:    16000,
			System:       []anthropic.BetaTextBlockParam{{Text: system}},
			Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user))},
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		}
		// Серверный fallback: если модель откажет по политике, запрос сам уйдёт на подходящую модель.
		// У Haiku серверного fallback нет.
		if !strings.Contains(cfg.Model, "haiku") {
			params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
			params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		}
		msg, err := client.Beta.Messages.New(ctx, params)
		if err != nil {
			return "", err
		}
		if msg.StopReason == anthropic.BetaStopReasonRefusal {
			return "", fmt.Errorf("Claude отказался переводить эту пачку строк")
		}
		if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
			return "", fmt.Errorf("ответ Claude обрезан по max_tokens — уменьши размер пачки")
		}
		var sb strings.Builder
		for _, b := range msg.Content {
			if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
				sb.WriteString(t.Text)
			}
		}
		return sb.String(), nil
	}
	return &llm{cfg: cfg, call: call, batch: 60, workers: 6}
}
