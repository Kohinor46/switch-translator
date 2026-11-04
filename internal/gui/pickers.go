package gui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"github.com/ncruces/zenity"
)

// Системные диалоги выбора: Finder на macOS, Проводник на Windows, zenity/kdialog на Linux.
// Если системного нет — встроенный диалог Fyne.

type pickKind int

const (
	pickFile pickKind = iota
	pickDir
	pickSave
)

type pickRequest struct {
	kind     pickKind
	title    string
	current  string   // что сейчас в поле: диалог откроется рядом
	patterns []string // например "*.nsp", "*.nsz"
	filename string   // для сохранения: имя по умолчанию
	btn      fyne.Disableable
	done     func(path string)
	doneAll  func(paths []string) // вместо done: можно выбрать сразу несколько файлов
}

func (u *ui) pick(r pickRequest) {
	opts := []zenity.Option{zenity.Title(r.title)}
	switch r.kind {
	case pickDir:
		opts = append(opts, zenity.Directory())
	case pickSave:
		opts = append(opts, zenity.ConfirmOverwrite())
	}
	if len(r.patterns) > 0 {
		opts = append(opts, zenity.FileFilter{Patterns: r.patterns, CaseFold: true})
	}
	// zenity не открывается в несуществующей папке — это выглядело бы как «нет системного диалога».
	start := startDir(r.current)
	switch {
	case r.kind == pickSave && start != "":
		opts = append(opts, zenity.Filename(filepath.Join(start, r.filename)))
	case r.kind == pickSave:
		opts = append(opts, zenity.Filename(r.filename))
	case start != "":
		opts = append(opts, zenity.Filename(start+string(os.PathSeparator)))
	}
	opts = append(opts, parentWindow(u.win)...)

	if r.btn != nil {
		r.btn.Disable()
	}
	go func() {
		// системный диалог блокирует — в UI-потоке его открывать нельзя
		var paths []string
		var err error
		switch {
		case r.kind == pickSave:
			var p string
			p, err = zenity.SelectFileSave(opts...)
			paths = []string{p}
		case r.doneAll != nil:
			paths, err = zenity.SelectFileMultiple(opts...)
		default:
			var p string
			p, err = zenity.SelectFile(opts...)
			paths = []string{p}
		}
		fyne.Do(func() {
			if r.btn != nil {
				r.btn.Enable()
			}
			switch {
			case err == nil && r.doneAll != nil:
				r.doneAll(paths)
			case err == nil:
				r.done(paths[0])
			case errors.Is(err, zenity.ErrCanceled):
			default:
				u.logf("Системный диалог недоступен (%v), открываю встроенный", err)
				u.pickFyne(r)
			}
		})
	}()
}

// pickFyne — встроенный диалог Fyne на случай, когда системного нет (в нём файл — только один).
func (u *ui) pickFyne(r pickRequest) {
	if r.doneAll != nil {
		r.done = func(p string) { r.doneAll([]string{p}) }
	}
	var d *dialog.FileDialog
	switch r.kind {
	case pickDir:
		d = dialog.NewFolderOpen(func(l fyne.ListableURI, err error) {
			if err == nil && l != nil {
				r.done(l.Path())
			}
		}, u.win)
	case pickSave:
		d = dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
			if err == nil && w != nil {
				w.Close()
				r.done(w.URI().Path())
			}
		}, u.win)
		d.SetFileName(r.filename)
	default:
		d = dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err == nil && rc != nil {
				rc.Close()
				r.done(rc.URI().Path())
			}
		}, u.win)
		if len(r.patterns) > 0 {
			exts := make([]string, len(r.patterns))
			for i, p := range r.patterns {
				exts[i] = strings.TrimPrefix(p, "*")
			}
			d.SetFilter(storage.NewExtensionFileFilter(exts))
		}
	}
	if start := startDir(r.current); start != "" {
		if l, err := storage.ListerForURI(storage.NewFileURI(start)); err == nil {
			d.SetLocation(l)
		}
	}
	d.Show() // окно диалога появляется только после Show, поэтому Resize — после
	d.Resize(fyne.NewSize(900, 600))
}

// startDir — папка, в которой открыть диалог: сама p (если это папка), папка файла p
// или ближайшая существующая родительская; "" для пустого или относительного пути
// (у приложения macOS рабочая папка — «/», пользователь имел в виду не её).
func startDir(p string) string {
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	for {
		if isDir(p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
