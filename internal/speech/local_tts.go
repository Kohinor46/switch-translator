package speech

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Локальные нейросетевые голоса — без интернета и бесплатно:
//   - Piper: лёгкие голоса VITS. Свежих готовых программ Piper под Mac на Apple Silicon нет, поэтому
//     голоса Piper запускает sherpa-onnx (k2-fsa) — у него есть готовые сборки под Mac, Windows и Linux,
//     а голоса Piper упакованы в его формате. Всё скачивается кнопкой;
//   - Silero: лучшие бесплатные русские голоса, с ударениями и «ё». Нужен Python с torch;
//   - XTTS-v2 (Coqui): копирует голос по нескольким секундам образца. Нужен Python с torch;
//   - RHVoice: лёгкий классический синтез — Linux (RHVoice-test) и Windows (голоса SAPI).

// ---------- Piper через sherpa-onnx ----------

const sherpaVersion = "v2.0.1"

func sherpaAsset() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64", "darwin/amd64":
		return "osx-universal2-shared", nil
	case "windows/amd64":
		return "win-x64-shared-MT-Release", nil
	case "linux/amd64":
		return "linux-x64-shared", nil
	case "linux/arm64":
		return "linux-aarch64-shared-cpu", nil
	}
	return "", fmt.Errorf("Piper: готовой сборки для %s/%s нет", runtime.GOOS, runtime.GOARCH)
}

func sherpaDir() string { return filepath.Join(ToolsDir(), "sherpa-onnx") }

func sherpaExe() string {
	name := "sherpa-onnx-offline-tts"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(sherpaDir(), "bin", name)
}

// piperVoices — русские голоса Piper (все — качества medium, 22 кГц).
var piperVoices = []struct{ ID, Label string }{
	{"irina", "Ирина, женский"},
	{"denis", "Денис, мужской"},
	{"dmitri", "Дмитрий, мужской"},
	{"ruslan", "Руслан, мужской, некоммерческая лицензия"},
}

// piperName — полное имя голоса: «irina» → «ru_RU-irina-medium»; полное имя (en_US-amy-medium) — как есть.
func piperName(voice string) string {
	v := VoiceID(voice)
	if v == "" {
		v = "irina"
	}
	if strings.Contains(v, "_") {
		return v
	}
	return "ru_RU-" + v + "-medium"
}

func piperVoiceDir(voice string) string {
	return filepath.Join(ToolsDir(), "piper-voices", piperName(voice))
}

// piperModel — файл .onnx голоса ("" — не скачан).
func piperModel(voice string) string {
	m, _ := filepath.Glob(filepath.Join(piperVoiceDir(voice), "*.onnx"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func setupPiper(ctx context.Context, voice string, log func(string), progress func(done, total int64)) error {
	if !fileExists(sherpaExe()) {
		asset, err := sherpaAsset()
		if err != nil {
			return err
		}
		log("Скачиваю sherpa-onnx " + sherpaVersion + " — он запускает голоса Piper")
		url := fmt.Sprintf("https://github.com/k2-fsa/sherpa-onnx/releases/download/%s/sherpa-onnx-%s-%s.tar.bz2", sherpaVersion, sherpaVersion, asset)
		if err := downloadTarBz2(ctx, url, sherpaDir(), progress); err != nil {
			return fmt.Errorf("не удалось скачать sherpa-onnx: %w", err)
		}
		if !fileExists(sherpaExe()) {
			return fmt.Errorf("в архиве sherpa-onnx нет программы %s", filepath.Base(sherpaExe()))
		}
	}
	if piperModel(voice) == "" {
		name := piperName(voice)
		log("Скачиваю голос " + name)
		url := "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-" + name + ".tar.bz2"
		if err := downloadTarBz2(ctx, url, piperVoiceDir(voice), progress); err != nil {
			return fmt.Errorf("не удалось скачать голос %s: %w", name, err)
		}
	}
	return nil
}

type piperTTS struct{ cfg Config }

func (p *piperTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	model := piperModel(p.cfg.Model)
	if !fileExists(sherpaExe()) || model == "" {
		return "", fmt.Errorf("Piper: голос не скачан — нажми «Скачать Piper и голос»")
	}
	dir := filepath.Dir(model)
	out := outBase + ".wav"
	cmd := exec.CommandContext(ctx, sherpaExe(),
		"--vits-model="+model,
		"--vits-tokens="+filepath.Join(dir, "tokens.txt"),
		"--vits-data-dir="+filepath.Join(dir, "espeak-ng-data"),
		"--num-threads=2",
		"--output-filename="+out,
		text)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("Piper: %v: %s", err, lastLine(b))
	}
	if !fileExists(out) {
		return "", fmt.Errorf("Piper не записал звук")
	}
	return out, nil
}

// downloadTarBz2 скачивает архив .tar.bz2 и распаковывает в dst без верхней папки архива.
// Сначала — во временную папку рядом: недокачанное не выглядит готовым.
func downloadTarBz2(ctx context.Context, url, dst string, progress func(done, total int64)) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return untarBz2(&progressReader{r: resp.Body, total: resp.ContentLength, fn: progress}, dst)
}

// untarBz2 распаковывает .tar.bz2 в dst без верхней папки архива.
func untarBz2(r io.Reader, dst string) error {
	tmp := dst + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// без верхней папки; пути только внутрь
		_, rel, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || rel == "" {
			continue
		}
		rel = filepath.FromSlash(rel)
		if !filepath.IsLocal(rel) {
			continue
		}
		p := filepath.Join(tmp, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || strings.Contains(h.Linkname, "..") {
				continue
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	os.RemoveAll(dst)
	return os.Rename(tmp, dst)
}

// ---------- Silero ----------

const sileroModel = "v5_6_ru"

func sileroPath() string { return filepath.Join(ToolsDir(), "silero", sileroModel+".pt") }

var sileroVoices = []struct{ ID, Label string }{
	{"xenia", "Ксения, женский"},
	{"baya", "Бая, женский"},
	{"kseniya", "Ксения (другая), женский"},
	{"aidar", "Айдар, мужской"},
	{"eugene", "Евгений, мужской"},
}

const sileroScript = `
import os, sys, json, wave
M = "@@SWTR "
def reply(d):
    sys.stdout.write(M + json.dumps(d, ensure_ascii=False) + "\n"); sys.stdout.flush()
try:
    import torch
    model = torch.package.PackageImporter(os.environ["SWTR_MODEL"]).load_pickle("tts_models", "model")
    model.to(torch.device("cpu"))
    err = None
except Exception as e:
    err = "Silero: модель не загрузилась: %s" % e
for line in sys.stdin:
    if err:
        reply({"error": err}); continue
    try:
        r = json.loads(line)
        audio = model.apply_tts(text=r["text"], speaker=r["voice"], sample_rate=48000, put_accent=True, put_yo=True)
        pcm = (audio.clamp(-1, 1) * 32767).to(torch.int16).numpy().tobytes()
        with wave.open(r["out"], "wb") as w:
            w.setnchannels(1); w.setsampwidth(2); w.setframerate(48000); w.writeframes(pcm)
        reply({"ok": True})
    except Exception as e:
        reply({"error": "Silero: %s" % e})
`

func setupSilero(ctx context.Context, log func(string), progress func(done, total int64)) error {
	if !pythonReady("silero") {
		if err := installPython(ctx, []string{"torch", "numpy"}, log); err != nil {
			return err
		}
		markPython("silero")
	}
	if !fileExists(sileroPath()) {
		log("Скачиваю модель Silero " + sileroModel + " (150 МБ)")
		if err := downloadFile(ctx, "https://models.silero.ai/models/tts/ru/"+sileroModel+".pt", sileroPath(), progress); err != nil {
			return fmt.Errorf("не удалось скачать модель Silero: %w", err)
		}
	}
	return nil
}

type sileroTTS struct{ cfg Config }

func (s *sileroTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	if !pythonReady("silero") || !fileExists(sileroPath()) {
		return "", fmt.Errorf("Silero не установлен — нажми «Установить Silero»")
	}
	voice := VoiceID(s.cfg.Model)
	if voice == "" {
		voice = "xenia"
	}
	out := outBase + ".wav"
	_, err := pyCall(ctx, "silero", sileroScript, []string{"SWTR_MODEL=" + sileroPath()},
		map[string]string{"text": text, "voice": voice, "out": out})
	return out, err
}

// ---------- XTTS-v2 ----------

// xttsSpeakers — встроенные голоса XTTS-v2 (их можно заставить говорить по-русски).
var xttsSpeakers = []string{"Ana Florence", "Claribel Dervla", "Daisy Studious", "Gracie Wise", "Tammie Ema", "Alison Dietlinde",
	"Annmarie Nele", "Asya Anara", "Brenda Stern", "Gitta Nikolina", "Henriette Usha", "Sofia Hellen", "Tammy Grit", "Tanja Adelina",
	"Vjollca Johnnie", "Nova Hogarth", "Maja Ruoho", "Uta Obando", "Lidiya Szekeres", "Chandra MacFarland", "Szofi Granger",
	"Camilla Holmström", "Lilya Stainthorpe", "Zofija Kendrick", "Narelle Moon", "Barbora MacLean", "Alexandra Hisakawa",
	"Alma María", "Rosemary Okafor", "Andrew Chipper", "Badr Odhiambo", "Dionisio Schuyler", "Royston Min", "Viktor Eka",
	"Abrahan Mack", "Adde Michal", "Baldur Sanjin", "Craig Gutsy", "Damien Black", "Gilberto Mathias", "Ilkin Urbano",
	"Kazuhiko Atallah", "Ludvig Milivoj", "Suad Qasim", "Torcull Diarmuid", "Viktor Menelaos", "Zacharie Aimilios",
	"Ige Behringer", "Filip Traverse", "Damjan Chapman", "Wulf Carlevaro", "Aaron Dreschner", "Kumar Dahl", "Eugenio Mataracı",
	"Ferran Simen", "Xavier Hayasaka", "Luis Moray", "Marcos Rudaski"}

const xttsScript = `
import os, sys, json
M = "@@SWTR "
def reply(d):
    sys.stdout.write(M + json.dumps(d, ensure_ascii=False) + "\n"); sys.stdout.flush()
try:
    import torch
    from TTS.api import TTS
    dev = "cuda" if torch.cuda.is_available() else "cpu"
    tts = TTS("tts_models/multilingual/multi-dataset/xtts_v2").to(dev)
    err = None
except BaseException as e:
    err = "XTTS: модель не загрузилась: %s" % e
for line in sys.stdin:
    if err:
        reply({"error": err}); continue
    try:
        r = json.loads(line)
        if r.get("op") == "ping":
            reply({"ok": True}); continue
        kw = dict(text=r["text"], file_path=r["out"], language=r["lang"])
        if r.get("wavs"):
            kw["speaker_wav"] = r["wavs"]
        else:
            kw["speaker"] = r["voice"]
        tts.tts_to_file(**kw)
        reply({"ok": True})
    except Exception as e:
        reply({"error": "XTTS: %s" % e})
`

// Лицензию модели (CPML, только некоммерческое использование) пользователь принимает кнопкой
// «Установить XTTS» — об этом сказано в инструкции; без этой переменной модель спрашивает согласие в консоли.
var xttsEnv = []string{"COQUI_TOS_AGREED=1"}

func xttsVoicesDir() string { return filepath.Join(ToolsDir(), "xtts-voices") }

func setupXTTS(ctx context.Context, log func(string)) error {
	if !pythonReady("xtts") {
		if err := installPython(ctx, []string{"torch", "torchaudio", "torchcodec", "coqui-tts", "six"}, log); err != nil {
			return err
		}
	}
	log("Загружаю модель XTTS-v2 (около 1,9 ГБ при первом запуске)")
	if err := pyPing(ctx, "xtts", xttsScript, xttsEnv, log); err != nil {
		return err
	}
	markPython("xtts")
	return nil
}

// xttsLang — код языка для XTTS.
func xttsLang(lang string) string {
	if lang == "zh" {
		return "zh-cn"
	}
	if lang == "" {
		return "ru"
	}
	return lang
}

type xttsTTS struct{ cfg Config }

func (x *xttsTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	if !pythonReady("xtts") {
		return "", fmt.Errorf("XTTS не установлен — нажми «Установить XTTS»")
	}
	voice := VoiceID(x.cfg.Model)
	if voice == "" {
		voice = xttsSpeakers[0]
	}
	out := outBase + ".wav"
	req := map[string]any{"text": text, "out": out, "lang": xttsLang(x.cfg.Lang), "voice": voice}
	if name, ok := strings.CutPrefix(voice, "clone:"); ok {
		wavs, _ := filepath.Glob(filepath.Join(xttsVoicesDir(), name, "*.wav"))
		if len(wavs) == 0 {
			return "", fmt.Errorf("XTTS: нет образцов голоса «%s» — скопируй голос заново", name)
		}
		req["wavs"] = wavs
	}
	_, err := pyCall(ctx, "xtts", xttsScript, xttsEnv, req)
	return out, err
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}_ -]+`)

// cloneXTTS сохраняет образцы речи как голос XTTS: копировать по ним голос модель умеет сама при озвучке.
func cloneXTTS(name string, samples []string) (string, error) {
	safe := strings.TrimSpace(unsafeName.ReplaceAllString(name, ""))
	if safe == "" {
		safe = "голос"
	}
	dir := filepath.Join(xttsVoicesDir(), safe)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// XTTS хватает 6–30 секунд; больше образцов — дольше каждая фраза
	for i, s := range samples[:min(len(samples), 10)] {
		b, err := os.ReadFile(s)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.wav", i)), b, 0o644); err != nil {
			return "", err
		}
	}
	return VoiceLabel(safe+", скопированный", "clone:"+safe), nil
}

func xttsVoices() []string {
	var out []string
	dirs, _ := os.ReadDir(xttsVoicesDir())
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, VoiceLabel(d.Name()+", скопированный", "clone:"+d.Name()))
		}
	}
	return append(out, xttsSpeakers...)
}

// ---------- RHVoice ----------

// rhvoiceRu — русские голоса RHVoice.
var rhvoiceRu = []string{"aleksandr", "aleksandr-hq", "anna", "arina", "artemiy", "elena", "evgeniy-rus", "irina", "mikhail",
	"pavel", "tatiana", "timofey", "umka", "victoria", "vitaliy", "vitaliy-ng", "vsevolod", "yuriy"}

func rhvoiceVoices() []string {
	switch runtime.GOOS {
	case "windows":
		// RHVoice под Windows ставит голоса SAPI: «Aleksandr», «Anna»…
		known := map[string]bool{}
		for _, v := range rhvoiceRu {
			known[v] = true
		}
		var out []string
		for _, v := range windowsVoices("") {
			if known[strings.ToLower(strings.SplitN(v, "+", 2)[0])] {
				out = append(out, v)
			}
		}
		return out
	case "linux":
		if dirs, err := os.ReadDir("/usr/share/RHVoice/voices"); err == nil {
			var out []string
			for _, d := range dirs {
				if d.IsDir() {
					out = append(out, d.Name())
				}
			}
			sort.Strings(out)
			return out
		}
		return rhvoiceRu
	}
	return nil
}

type rhvoiceTTS struct{ cfg Config }

func (r *rhvoiceTTS) Synthesize(ctx context.Context, text, outBase string) (string, error) {
	voice := VoiceID(r.cfg.Model)
	if runtime.GOOS == "windows" {
		return (&windowsTTS{Config{Model: voice}}).Synthesize(ctx, text, outBase)
	}
	bin, err := findTool("RHVoice-test")
	if err != nil {
		return "", fmt.Errorf("RHVoice не установлен — см. инструкцию")
	}
	if voice == "" {
		voice = "aleksandr"
	}
	out := outBase + ".wav"
	cmd := exec.CommandContext(ctx, bin, "-p", voice, "-o", out)
	cmd.Stdin = strings.NewReader(text)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("RHVoice: %v: %s", err, lastLine(b))
	}
	return out, nil
}

// ---------- общее ----------

// pythonReady — окружение Python для движка поставлено (пометка — после удачной установки:
// проверять импорт torch при каждом взгляде на вкладку слишком долго).
func pythonReady(engine string) bool {
	return fileExists(venvPython()) && fileExists(filepath.Join(PythonEnv(), ".ok-"+engine))
}

func markPython(engine string) {
	os.WriteFile(filepath.Join(PythonEnv(), ".ok-"+engine), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// downloadFile качает файл во временный рядом и потом переименовывает: прерванная загрузка не оставляет битый файл.
func downloadFile(ctx context.Context, url, dst string, progress func(done, total int64)) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
