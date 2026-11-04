package translate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Перевод через установленные Codex и Claude Code: они работают в счёт подписки
// пользователя, без API-ключа. Каждая пачка строк — отдельный неинтерактивный запуск.

// FindCodex ищет codex в PATH и внутри приложения ChatGPT.
func FindCodex() (string, error) {
	return findCLI("Codex не найден: установи приложение ChatGPT или выполни «npm install -g @openai/codex»",
		[]string{"codex"},
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
	)
}

// FindClaudeCode ищет claude в PATH, в стандартных местах установки и внутри приложения Claude.
func FindClaudeCode() (string, error) {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".claude", "local", "claude"),
	}
	// Claude для Mac хранит Claude Code по версиям — берём самую новую.
	bundled, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "Claude", "claude-code", "*", "*", "claude.app", "Contents", "MacOS", "claude"))
	sort.Sort(sort.Reverse(sort.StringSlice(bundled)))
	paths = append(paths, bundled...)
	return findCLI("Claude Code не найден: установи приложение Claude или выполни команду установки из инструкции",
		[]string{"claude"}, paths...)
}

func findCLI(notFound string, names []string, paths ...string) (string, error) {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if p := filepath.Join(dir, n); isExec(p) {
				return p, nil
			}
		}
	}
	for _, p := range paths {
		if isExec(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s", notFound)
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

func codexCLI(bin string) func(ctx context.Context, cfg Config, system, user string) (string, error) {
	return func(ctx context.Context, cfg Config, system, user string) (string, error) {
		dir, err := os.MkdirTemp("", "swtr-codex-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "answer.txt")
		args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "--color", "never", "-o", out}
		if cfg.Model != "" {
			args = append(args, "-m", cfg.Model)
		}
		args = append(args, "-") // текст задания — из stdin
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(system + "\n\n" + user)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := lastLines(stderr.String())
			if strings.Contains(strings.ToLower(msg), "login") || strings.Contains(msg, "401") {
				return "", fmt.Errorf("Codex не вошёл в аккаунт: открой Терминал и выполни «%s login»", bin)
			}
			return "", fmt.Errorf("codex: %v: %s", err, msg)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return "", fmt.Errorf("codex не вернул ответ: %s", lastLines(stderr.String()))
		}
		return string(b), nil
	}
}

func claudeCLI(bin string) func(ctx context.Context, cfg Config, system, user string) (string, error) {
	return func(ctx context.Context, cfg Config, system, user string) (string, error) {
		// для перевода не нужны ни инструменты, ни MCP-серверы, ни долгие размышления —
		// без них каждый запуск заметно быстрее
		args := []string{"-p", "--output-format", "text", "--no-session-persistence", "--system-prompt", system,
			"--strict-mcp-config", "--tools", "", "--disable-slash-commands", "--effort", "low"}
		if cfg.Model != "" {
			args = append(args, "--model", cfg.Model)
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = os.TempDir()
		cmd.Stdin = strings.NewReader(user)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := lastLines(stderr.String() + "\n" + stdout.String())
			if strings.Contains(msg, "Not logged in") || strings.Contains(msg, "/login") {
				return "", fmt.Errorf("Claude Code не вошёл в аккаунт: открой Терминал, запусти «%s» и выполни /login", bin)
			}
			return "", fmt.Errorf("claude: %v: %s", err, msg)
		}
		return stdout.String(), nil
	}
}

func lastLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}
