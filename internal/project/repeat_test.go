package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"switch-translator/internal/gametext"
)

type countTr struct{ sent []string }

func (c *countTr) Translate(_ context.Context, texts []string) ([]string, error) {
	out := make([]string, len(texts))
	for i, t := range texts {
		c.sent = append(c.sent, t)
		out[i] = "ru:" + t
	}
	return out, nil
}

func (c *countTr) BatchSize() int { return 2 }

// Одинаковые реплики уходят переводчику один раз, уже переведённый текст не переводится заново.
func TestTranslateRepeats(t *testing.T) {
	s := &Session{WorkDir: t.TempDir(), log: func(string, ...any) {}}
	s.Strings = []gametext.String{
		{ID: "1", Source: "Gwhoaa..."}, {ID: "2", Source: "Gwhoaa..."}, {ID: "3", Source: "Back"},
		{ID: "4", Source: "Yawn...", Translation: "А-а-ах..."}, {ID: "5", Source: "Yawn..."},
		{ID: "6", Source: "Back"}, {ID: "7", Source: "Skip me", Skip: true},
	}
	tr := &countTr{}
	if _, err := s.TranslateText(context.Background(), tr, "ru", func(string, float64, float64) {}); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) != 2 {
		t.Fatalf("переводчику ушло %v", tr.sent)
	}
	want := map[string]string{"1": "ru:Gwhoaa...", "2": "ru:Gwhoaa...", "3": "ru:Back", "5": "А-а-ах...", "6": "ru:Back", "7": ""}
	for _, str := range s.Strings {
		if w, ok := want[str.ID]; ok && str.Translation != w {
			t.Errorf("%s: %q", str.ID, str.Translation)
		}
	}
}

type flakyTr struct {
	countTr
	fails int
}

func (f *flakyTr) Translate(ctx context.Context, texts []string) ([]string, error) {
	if f.fails > 0 {
		f.fails--
		return nil, errors.New("API Error: Connection dropped (ECONNRESET)")
	}
	return f.countTr.Translate(ctx, texts)
}

// Обрыв связи — не конец перевода: пачка переводится заново.
func TestTranslateRetry(t *testing.T) {
	defer func(w []time.Duration) { retryWaits = w }(retryWaits)
	retryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	var waited []time.Duration
	tr := &flakyTr{fails: 1}
	res, err := translateRetry(context.Background(), tr, []string{"Hi"}, func(w time.Duration, _ error) { waited = append(waited, w) })
	if err != nil || res[0] != "ru:Hi" || len(waited) != 1 {
		t.Fatalf("%v %v %v", res, err, waited)
	}
	// три обрыва подряд — сдаёмся
	if _, err := translateRetry(context.Background(), &flakyTr{fails: 3}, []string{"Hi"}, func(time.Duration, error) {}); err == nil {
		t.Fatal("ошибка потерялась")
	}
}
