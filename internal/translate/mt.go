package translate

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type myMemory struct{ cfg Config }

func (m *myMemory) BatchSize() int { return 1 }

// MyMemory принимает до 500 байт за запрос — длинные реплики переводим по строкам.
const myMemoryMax = 480

func (m *myMemory) Translate(ctx context.Context, texts []string) ([]string, error) {
	return mtBatch(ctx, texts, func(ctx context.Context, in []string) ([]string, error) {
		out := make([]string, len(in))
		for i, t := range in {
			if len(t) <= myMemoryMax {
				r, err := m.one(ctx, t)
				if err != nil {
					return nil, err
				}
				out[i] = r
				continue
			}
			lines := strings.Split(t, "\n")
			for j, line := range lines {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if len(line) > myMemoryMax {
					return nil, fmt.Errorf("MyMemory: строка длиннее %d байт, такую он не переведёт — выбери другой переводчик", myMemoryMax)
				}
				r, err := m.one(ctx, line)
				if err != nil {
					return nil, err
				}
				lines[j] = r
			}
			out[i] = strings.Join(lines, "\n")
		}
		return out, nil
	})
}

func (m *myMemory) one(ctx context.Context, text string) (string, error) {
	q := url.Values{"q": {text}, "langpair": {m.cfg.From + "|" + m.cfg.To}}
	if strings.Contains(m.cfg.APIKey, "@") {
		q.Set("de", m.cfg.APIKey)
	}
	var result string
	err := withRetry(ctx, func() error {
		var resp struct {
			ResponseData struct {
				TranslatedText string `json:"translatedText"`
			} `json:"responseData"`
			ResponseStatus  any    `json:"responseStatus"`
			ResponseDetails string `json:"responseDetails"`
			QuotaFinished   bool   `json:"quotaFinished"`
		}
		if err := doJSON(ctx, "GET", "https://api.mymemory.translated.net/get?"+q.Encode(), nil, nil, &resp); err != nil {
			return err
		}
		tr := resp.ResponseData.TranslatedText
		if resp.QuotaFinished || strings.HasPrefix(strings.ToUpper(tr), "MYMEMORY WARNING") {
			return fmt.Errorf("дневной лимит MyMemory исчерпан. Впиши свой e-mail в поле «Ключ» (лимит 50 000 символов) или выбери другой переводчик")
		}
		switch s := fmt.Sprint(resp.ResponseStatus); s {
		case "200":
		case "429":
			return retryable{fmt.Errorf("MyMemory: слишком частые запросы")}
		default:
			return fmt.Errorf("MyMemory: %s %s", s, resp.ResponseDetails)
		}
		result = tr
		return nil
	})
	time.Sleep(300 * time.Millisecond) // бесплатный API не любит частые запросы
	return result, err
}

type libre struct{ cfg Config }

func (l *libre) BatchSize() int { return 25 }

func (l *libre) Translate(ctx context.Context, texts []string) ([]string, error) {
	return mtBatch(ctx, texts, func(ctx context.Context, in []string) ([]string, error) {
		body := map[string]any{"q": in, "source": l.cfg.From, "target": l.cfg.To, "format": "text"}
		if l.cfg.APIKey != "" {
			body["api_key"] = l.cfg.APIKey
		}
		var resp struct {
			TranslatedText []string `json:"translatedText"`
		}
		err := doJSON(ctx, "POST", strings.TrimRight(l.cfg.URL, "/")+"/translate", nil, body, &resp)
		return resp.TranslatedText, err
	})
}

type deepL struct{ cfg Config }

func (d *deepL) BatchSize() int { return 50 }
func (d *deepL) Workers() int   { return 2 }

func (d *deepL) Translate(ctx context.Context, texts []string) ([]string, error) {
	host := "https://api.deepl.com"
	if strings.HasSuffix(d.cfg.APIKey, ":fx") {
		host = "https://api-free.deepl.com"
	}
	return mtBatch(ctx, texts, func(ctx context.Context, in []string) ([]string, error) {
		body := map[string]any{
			"text":        in,
			"source_lang": strings.ToUpper(d.cfg.From),
			"target_lang": strings.ToUpper(d.cfg.To),
		}
		var resp struct {
			Translations []struct {
				Text string `json:"text"`
			} `json:"translations"`
		}
		if err := doJSON(ctx, "POST", host+"/v2/translate", map[string]string{"Authorization": "DeepL-Auth-Key " + d.cfg.APIKey}, body, &resp); err != nil {
			return nil, err
		}
		out := make([]string, len(resp.Translations))
		for i, t := range resp.Translations {
			out[i] = t.Text
		}
		return out, nil
	})
}

type google struct{ cfg Config }

func (g *google) BatchSize() int { return 100 }
func (g *google) Workers() int   { return 4 }

func (g *google) Translate(ctx context.Context, texts []string) ([]string, error) {
	return mtBatch(ctx, texts, func(ctx context.Context, in []string) ([]string, error) {
		body := map[string]any{"q": in, "source": g.cfg.From, "target": g.cfg.To, "format": "text"}
		var resp struct {
			Data struct {
				Translations []struct {
					TranslatedText string `json:"translatedText"`
				} `json:"translations"`
			} `json:"data"`
		}
		u := "https://translation.googleapis.com/language/translate/v2"
		if err := doJSON(ctx, "POST", u, map[string]string{"X-Goog-Api-Key": g.cfg.APIKey}, body, &resp); err != nil {
			return nil, err
		}
		out := make([]string, len(resp.Data.Translations))
		for i, t := range resp.Data.Translations {
			out[i] = t.TranslatedText
		}
		return out, nil
	})
}
