//go:build !windows

package gui

import (
	"fyne.io/fyne/v2"
	"github.com/ncruces/zenity"
)

// parentWindow: на macOS привязка к окну потребовала бы разрешения «Автоматизация»,
// поэтому панель открывается отдельно; на Linux Fyne не отдаёт id окна X11.
func parentWindow(fyne.Window) []zenity.Option { return nil }
