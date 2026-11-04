// i18n собирает из исходников программы все строки с русскими буквами — ключи переводов
// интерфейса — и проверяет файлы переводов internal/i18n/locales/<язык>.json.
//
//	go run ./tools/i18n keys     — ключи (JSON-массив) в stdout
//	go run ./tools/i18n missing  — каких ключей не хватает в каждом языке (выход 1, если не хватает)
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// пакеты, строки которых видит пользователь
var dirs = []string{"internal/gui", "internal/project", "internal/engine", "internal/nx", "internal/translate",
	"internal/speech", "internal/audio", "internal/video", "internal/gametext", "internal/ffmpeg", "internal/keys",
	"internal/whisper", "internal/unreal", "internal/unity", "internal/ttgames", "internal/codec", "internal/bffnt",
	"internal/oodle", "internal/update"}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "использование: i18n keys|missing")
		os.Exit(2)
	}
	keys := collect()
	switch os.Args[1] {
	case "keys":
		b, _ := json.MarshalIndent(keys, "", "  ")
		os.Stdout.Write(append(b, '\n'))
	case "missing":
		files, _ := filepath.Glob("internal/i18n/locales/*.json")
		bad := false
		for _, f := range files {
			var m map[string]string
			b, _ := os.ReadFile(f)
			if err := json.Unmarshal(b, &m); err != nil {
				fmt.Printf("%s: %v\n", f, err)
				bad = true
				continue
			}
			n := 0
			for _, k := range keys {
				if _, ok := m[k]; !ok {
					n++
				}
			}
			if n > 0 {
				bad = true
			}
			fmt.Printf("%s: не хватает %d из %d\n", filepath.Base(f), n, len(keys))
		}
		if bad {
			os.Exit(1)
		}
	}
}

func collect() []string {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			ast.Inspect(af, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && skipCall(call) {
					return false
				}
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && hasCyrillic(s) && !isCode(s) {
					seen[s] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// skipCall — вызовы, строки которых не показываются: регулярные выражения.
func skipCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "regexp"
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// isCode — встроенные в программу скрипты Python: их строки не переводятся.
func isCode(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "import ") || strings.Contains(s, "\ndef ")
}
