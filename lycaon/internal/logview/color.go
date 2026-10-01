package logview

import (
	"os"
	"strconv"
)

// Palette renders semantic styles as ANSI escapes, or as plain text when disabled.
// Disabled palettes make rendering deterministic for tests and respect non-TTY
// output and the NO_COLOR convention.
type Palette struct{ enabled bool }

// NewPalette enables color only when requested and NO_COLOR is unset.
func NewPalette(enabled bool) Palette {
	if os.Getenv("NO_COLOR") != "" {
		enabled = false
	}
	return Palette{enabled: enabled}
}

// IsTTY reports whether f is a terminal.
func IsTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (p Palette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (p Palette) Bold(s string) string     { return p.wrap("1", s) }
func (p Palette) Dim(s string) string      { return p.wrap("2", s) }
func (p Palette) Red(s string) string      { return p.wrap("31", s) }
func (p Palette) Green(s string) string    { return p.wrap("32", s) }
func (p Palette) Yellow(s string) string   { return p.wrap("33", s) }
func (p Palette) Blue(s string) string     { return p.wrap("34", s) }
func (p Palette) Magenta(s string) string  { return p.wrap("35", s) }
func (p Palette) Cyan(s string) string     { return p.wrap("36", s) }
func (p Palette) BoldCyan(s string) string { return p.wrap("1;36", s) }

// Role colors the message-role label in a transcript.
func (p Palette) Role(role string) string {
	switch role {
	case "system":
		return p.Magenta(role)
	case "user":
		return p.Green(role)
	case "assistant":
		return p.BoldCyan(role)
	case "tool":
		return p.Yellow(role)
	default:
		return p.Dim(role)
	}
}

// Status colors an HTTP status code by class.
func (p Palette) Status(code int) string {
	s := strconv.Itoa(code)
	switch {
	case code >= 500:
		return p.Red(s)
	case code >= 400:
		return p.Yellow(s)
	case code >= 300:
		return p.Cyan(s)
	case code >= 200:
		return p.Green(s)
	default:
		return p.Dim(s)
	}
}

// Topic colors an SSE topic for quick scanning of the stream.
func (p Palette) Topic(topic string) string {
	switch topic {
	case "message":
		return p.Cyan(topic)
	case "workflow":
		return p.Magenta(topic)
	case "board":
		return p.Blue(topic)
	case "scan":
		return p.Yellow(topic)
	case "session":
		return p.Green(topic)
	default:
		return p.Dim(topic)
	}
}
