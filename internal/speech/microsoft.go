package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Голоса Microsoft — два официальных пути:
//   - Azure AI Speech: нейросетевые голоса (для русского — Светлана, Дмитрий), бесплатный тариф F0 —
//     500 тысяч символов в месяц, нужен ключ и регион из портала Azure;
//   - голоса Windows: встроены в систему, бесплатно и без интернета, только под Windows.

// AzureRegions — регионы, где доступны нейросетевые голоса (самые частые).
var AzureRegions = []string{"westeurope", "northeurope", "eastus", "westus2", "southeastasia", "japaneast", "uksouth", "francecentral", "germanywestcentral"}

func azureURL(region, path string) string {
	return fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/%s", strings.TrimSpace(region), path)
}

// AzureVoices — нейросетевые голоса для языка (lang: «ru», «en»…).
func AzureVoices(ctx context.Context, key, region, lang string) ([]string, error) {
	if key == "" || region == "" {
		return nil, fmt.Errorf("Azure: нужны ключ и регион")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", azureURL(region, "voices/list"), nil)
	req.Header.Set("Ocp-Apim-Subscription-Key", key)
	data, err := do(req)
	if err != nil {
		return nil, err
	}
	var all []struct{ ShortName, Gender, Locale, LocalName, VoiceType string }
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range all {
		if !strings.HasPrefix(strings.ToLower(v.Locale), strings.ToLower(lang)+"-") || v.VoiceType != "Neural" {
			continue
		}
		g := map[string]string{"Female": "женский", "Male": "мужской"}[v.Gender]
		out = append(out, VoiceLabel(fmt.Sprintf("%s, %s", v.LocalName, g), v.ShortName))
	}
	sort.Strings(out)
	return out, nil
}

// azureDefault — голос, если пользователь ещё не выбрал.
func azureDefault(lang string) string {
	if v, ok := map[string]string{"ru": "ru-RU-SvetlanaNeural", "uk": "uk-UA-PolinaNeural", "ja": "ja-JP-NanamiNeural",
		"de": "de-DE-KatjaNeural", "fr": "fr-FR-DeniseNeural", "es": "es-ES-ElviraNeural"}[lang]; ok {
		return v
	}
	return "en-US-AriaNeural"
}

type azureTTS struct{ cfg Config }

func (a *azureTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(a.cfg.Model)
	if voice == "" {
		voice = azureDefault(a.cfg.Lang)
	}
	locale := strings.Join(strings.SplitN(voice, "-", 3)[:2], "-")
	ssml := fmt.Sprintf("<speak version='1.0' xml:lang='%s'><voice name='%s'>%s</voice></speak>", locale, voice, html.EscapeString(text))
	req, _ := http.NewRequestWithContext(ctx, "POST", azureURL(a.cfg.Region, "v1"), strings.NewReader(ssml))
	req.Header.Set("Ocp-Apim-Subscription-Key", a.cfg.APIKey)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", "riff-24khz-16bit-mono-pcm") // сразу WAV — его можно вписать в игру
	req.Header.Set("User-Agent", "switch-translator")
	data, err := do(req)
	if err != nil {
		return "", fmt.Errorf("Azure: %w", err)
	}
	out := outBase + ".wav"
	return out, os.WriteFile(out, data, 0o644)
}

// windowsVoices — голоса, установленные в Windows (System.Speech).
func windowsVoices(lang string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Add-Type -AssemblyName System.Speech; (New-Object System.Speech.Synthesis.SpeechSynthesizer).GetInstalledVoices() | ForEach-Object { $_.VoiceInfo.Name + '|' + $_.VoiceInfo.Culture.Name }`).Output()
	if err != nil {
		return nil
	}
	var voices []string
	for _, l := range strings.Split(string(out), "\n") {
		name, culture, ok := strings.Cut(strings.TrimSpace(l), "|")
		if ok && (lang == "" || strings.HasPrefix(strings.ToLower(culture), lang)) {
			voices = append(voices, name)
		}
	}
	return voices
}

// WindowsVoices — голоса Windows для языка.
func WindowsVoices(lang string) []string { return windowsVoices(lang) }

type windowsTTS struct{ cfg Config }

func (w *windowsTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	// текст — через файл: так не нужно экранировать кавычки и переводы строк для PowerShell
	dir, err := os.MkdirTemp("", "swtr-sapi-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	tf := filepath.Join(dir, "text.txt")
	if err := os.WriteFile(tf, []byte(text), 0o644); err != nil {
		return "", err
	}
	out := outBase + ".wav"
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := "Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; "
	if v := strings.TrimSpace(w.cfg.Model); v != "" {
		script += "$s.SelectVoice(" + q(v) + "); "
	}
	script += "$s.SetOutputToWaveFile(" + q(out) + "); $s.Speak([IO.File]::ReadAllText(" + q(tf) + ", [Text.Encoding]::UTF8)); $s.Dispose()"
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", script)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("голоса Windows: %v: %s", err, lastLine(stderr.Bytes()))
	}
	return out, nil
}
