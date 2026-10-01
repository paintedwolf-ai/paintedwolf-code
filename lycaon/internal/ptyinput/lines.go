package ptyinput

import (
	"strings"
	"unicode/utf8"
)

// Bounds on rendered terminal input.
const (
	maxLines     = 256
	maxLineBytes = 8192
)

// DecodeLines renders terminal input into command lines, applying basic line discipline edits.
func DecodeLines(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	expanded, err := ExpandControlInput(input)
	if err != nil {
		expanded = []byte(input)
	}
	lines := make([]string, 0, 4)
	cur := make([]byte, 0, len(expanded))
	flush := func() {
		if line := strings.TrimSpace(string(cur)); line != "" && len(lines) < maxLines {
			lines = append(lines, line)
		}
		cur = cur[:0]
	}
	for i := 0; i < len(expanded); i++ {
		c := expanded[i]
		switch {
		case c == '\r' || c == '\n':
			flush()
		case c == 0x7f || c == 0x08:
			cur = trimLastRune(cur)
		case c == 0x15: // Ctrl-U kills the line
			cur = cur[:0]
		case c == 0x17: // Ctrl-W erases the last word
			cur = trimLastWord(cur)
		case c == 0x03: // Ctrl-C discards what was typed
			cur = cur[:0]
		case c == 0x1b:
			i += escapeSequenceLength(expanded[i:]) - 1
		case c == '\t':
			// Completion expands in the shell. A space keeps token boundaries
			// intact without inventing the text the shell would insert.
			cur = append(cur, ' ')
		case c < 0x20:
		default:
			if len(cur) < maxLineBytes {
				cur = append(cur, c)
			}
		}
	}
	flush()
	return lines
}

// escapeSequenceLength reports how many bytes the escape sequence at the start of
// b occupies. CSI and SS3 sequences run to their final byte; anything else is the
// escape plus the byte it introduces.
func escapeSequenceLength(b []byte) int {
	if len(b) < 2 {
		return len(b)
	}
	if b[1] != '[' && b[1] != 'O' {
		return 2
	}
	for i := 2; i < len(b); i++ {
		if b[i] >= '@' && b[i] <= '~' {
			return i + 1
		}
	}
	return len(b)
}

func trimLastRune(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	_, size := utf8.DecodeLastRune(b)
	return b[:len(b)-size]
}

func trimLastWord(b []byte) []byte {
	end := len(b)
	for end > 0 && b[end-1] == ' ' {
		end--
	}
	for end > 0 && b[end-1] != ' ' {
		end--
	}
	return b[:end]
}
