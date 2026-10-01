package jsonfence

import (
	"strings"
)

// Block is a fenced code block.
type Block struct {
	// Language is the first word of the info string; empty when untagged.
	Language string
	Payload  string
}

// Trailing splits Markdown whose last block is a closed fenced code block into
// the Markdown above it and that block. Fences follow CommonMark: a run of at
// least three backticks or tildes indented at most three spaces, closed by a
// run of the same character at least as long with nothing after it. Fences
// inside earlier blocks are tracked, so a fence line quoted inside a code
// block never opens or closes one.
func Trailing(markdown string) (before string, block Block, ok bool) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(markdown, " \t\r\n"), "\r\n", "\n"), "\n")
	var (
		open     fence
		inside   bool
		openLine int
	)
	for i, line := range lines {
		if !inside {
			if f, ok := openingFence(line); ok {
				open, inside, openLine = f, true, i
			}
			continue
		}
		if !closesFence(line, open) {
			continue
		}
		inside = false
		if i == len(lines)-1 {
			return strings.Join(lines[:openLine], "\n"), Block{
				Language: open.language,
				Payload:  strings.Join(lines[openLine+1:i], "\n"),
			}, true
		}
	}
	return "", Block{}, false
}

type fence struct {
	char     byte
	length   int
	language string
}

// openingFence reads a fence opener. A backtick fence's info string may not
// contain a backtick, so inline code at the start of a line is not a fence.
func openingFence(line string) (fence, bool) {
	rest, ok := fenceIndent(line)
	if !ok {
		return fence{}, false
	}
	char, length := fenceRun(rest)
	if length < 3 {
		return fence{}, false
	}
	info := strings.TrimSpace(rest[length:])
	if char == '`' && strings.ContainsRune(info, '`') {
		return fence{}, false
	}
	language, _, _ := strings.Cut(info, " ")
	language, _, _ = strings.Cut(language, "\t")
	return fence{char: char, length: length, language: language}, true
}

func closesFence(line string, open fence) bool {
	rest, ok := fenceIndent(line)
	if !ok {
		return false
	}
	char, length := fenceRun(rest)
	return char == open.char && length >= open.length && strings.TrimSpace(rest[length:]) == ""
}

// fenceIndent strips the up to three spaces a fence line may be indented by.
func fenceIndent(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	return trimmed, len(line)-len(trimmed) <= 3
}

func fenceRun(s string) (byte, int) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	n := 1
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return s[0], n
}
