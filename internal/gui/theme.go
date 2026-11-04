package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// stTheme — стандартная тема Fyne с цветами, проверенными на контраст
// (WCAG: текст ≥ 4.5:1 к фону), и спокойной шкалой заголовков.
// fixed — всегда variant (светлая или тёмная), иначе как в системе.
type stTheme struct {
	variant fyne.ThemeVariant
	fixed   bool
}

func hex(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xFF}
}

// Токены: светлая / тёмная. Контраст указан к фону своей темы.
var palette = map[fyne.ThemeColorName][2]color.Color{
	theme.ColorNameBackground:  {hex(0xFFFFFF), hex(0x1C1C1E)},
	theme.ColorNameForeground:  {hex(0x1D1D1F), hex(0xF5F5F7)}, // 16.8:1 / 15.6:1
	theme.ColorNamePlaceHolder: {hex(0x6E6E73), hex(0xA1A1A6)}, // 5.1:1 / 6.6:1
	theme.ColorNamePrimary:     {hex(0x1F6FEB), hex(0x3D8BFF)}, // 4.6:1 / 5.1:1
	theme.ColorNameSuccess:     {hex(0x1A7F37), hex(0x3FB950)}, // 5.1:1 / 6.7:1
	theme.ColorNameWarning:     {hex(0x9A6700), hex(0xE3B341)}, // 4.9:1 / 8.7:1
	theme.ColorNameError:       {hex(0xD70015), hex(0xFF5A5F)}, // 5.4:1 / 5.6:1
}

func (t stTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if t.fixed {
		v = t.variant
	}
	if c, ok := palette[n]; ok {
		if v == theme.VariantDark {
			return c[1]
		}
		return c[0]
	}
	return theme.DefaultTheme().Color(n, v)
}

func (stTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (stTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

func (stTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameText:
		return 14
	case theme.SizeNameSubHeadingText:
		return 16
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameCaptionText:
		return 12
	}
	return theme.DefaultTheme().Size(n)
}
