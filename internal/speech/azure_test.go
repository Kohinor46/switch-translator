package speech

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Запрос к Azure: голос, язык, ключ и формат WAV — подменённый сервер проверяет, что программа шлёт.
func TestAzureRequest(t *testing.T) {
	var body, key, format string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, key, format = string(b), r.Header.Get("Ocp-Apim-Subscription-Key"), r.Header.Get("X-Microsoft-OutputFormat")
		w.Write([]byte("RIFF....WAVE"))
	}))
	defer srv.Close()
	old := client.Transport
	client.Transport = &rewrite{to: srv.URL, rt: srv.Client().Transport}
	defer func() { client.Transport = old }()
	tts, err := NewSynthesizer("azure", Config{APIKey: "k", Region: "westeurope", Lang: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tts.Synthesize(context.Background(), "Привет & <пока>", filepath.Join(t.TempDir(), "x"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if key != "k" || format != "riff-24khz-16bit-mono-pcm" || !strings.Contains(body, "ru-RU-SvetlanaNeural") ||
		!strings.Contains(body, "Привет &amp; &lt;пока&gt;") || string(b) != "RIFF....WAVE" {
		t.Fatalf("запрос: %q %q %q", key, format, body)
	}
}

type rewrite struct {
	to string
	rt http.RoundTripper
}

func (r *rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Scheme, u.Host = "https", strings.TrimPrefix(r.to, "https://")
	req2 := req.Clone(req.Context())
	req2.URL = &u
	return r.rt.RoundTrip(req2)
}
