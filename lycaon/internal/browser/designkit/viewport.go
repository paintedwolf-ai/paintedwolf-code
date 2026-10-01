package designkit

import (
	"fmt"
	"strings"
)

// ViewportPreset is a named mockup canvas size.
type ViewportPreset struct {
	Name   string
	Width  int
	Height int
}

// viewportPresets are the hermetic canvas sizes agents may request by name.
var viewportPresets = []ViewportPreset{
	{Name: "phone", Width: 390, Height: 844},
	{Name: "phone-landscape", Width: 844, Height: 390},
	{Name: "tablet", Width: 768, Height: 1024},
	{Name: "tablet-landscape", Width: 1024, Height: 768},
	{Name: "desktop", Width: 1280, Height: 720},
	{Name: "desktop-wide", Width: 1440, Height: 900},
}

var viewportByName = func() map[string]ViewportPreset {
	m := make(map[string]ViewportPreset, len(viewportPresets))
	for _, p := range viewportPresets {
		m[p.Name] = p
	}
	return m
}()

// ViewportPresetNames returns catalog preset names.
func ViewportPresetNames() []string {
	out := make([]string, len(viewportPresets))
	for i, p := range viewportPresets {
		out[i] = p.Name
	}
	return out
}

// LookupViewportPreset resolves a named viewport.
func LookupViewportPreset(name string) (ViewportPreset, bool) {
	p, ok := viewportByName[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// themes are host-injected color schemes.
var themes = []string{"light", "dark", "transparent"}

// Themes returns a copy of host-injected color schemes.
func Themes() []string {
	return append([]string(nil), themes...)
}

// NormalizeTheme returns light|dark or an error for unknown values.
func NormalizeTheme(theme string) (string, error) {
	theme = strings.ToLower(strings.TrimSpace(theme))
	if theme == "" {
		return "light", nil
	}
	for _, t := range themes {
		if theme == t {
			return theme, nil
		}
	}
	return "", fmt.Errorf("unknown theme %q", theme)
}
