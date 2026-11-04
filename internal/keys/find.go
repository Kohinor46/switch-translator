package keys

import (
	"os"
	"path/filepath"
	"runtime"
)

// Find ищет prod.keys там, где они обычно лежат: в ~/.switch и в папке switch
// на подключённой карте памяти (туда их сохраняет Lockpick_RCM).
// Возвращает найденные пути, сначала ~/.switch.
func Find() []string {
	var dirs []string
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".switch"))
	}
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, glob("/Volumes/*/switch")...)
	case "windows":
		for d := 'C'; d <= 'Z'; d++ { // A: и B: пропускаем — дисководы могут спросить дискету
			dirs = append(dirs, string(d)+`:\switch`)
		}
	default:
		user := os.Getenv("USER")
		dirs = append(dirs, glob("/media/"+user+"/*/switch")...)
		dirs = append(dirs, glob("/run/media/"+user+"/*/switch")...)
		dirs = append(dirs, glob("/mnt/*/switch")...)
	}
	var out []string
	for _, d := range dirs {
		p := filepath.Join(d, "prod.keys")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// TitleKeysNear возвращает title.keys из той же папки, что и prod.keys, если он там есть.
func TitleKeysNear(prodPath string) string {
	p := filepath.Join(filepath.Dir(prodPath), "title.keys")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}
