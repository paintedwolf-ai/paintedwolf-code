// Package ptyinput holds the named control tokens the terminal tools accept and
// renders a keystroke payload into the command lines a shell will read. The pty
// tool surface and the detection-pack event projection share its token table.
package ptyinput

import (
	"fmt"
	"strings"
)

// ExpandControlInput expands named control tokens into byte sequences. Tokens
// are written as {Name} (e.g. {Enter}, {Ctrl-C}) and may mix with literal text.
// controlTokenBytes holds the accepted set.
func ExpandControlInput(input string) ([]byte, error) {
	if input == "" {
		return nil, fmt.Errorf("input is required")
	}
	var out []byte
	for i := 0; i < len(input); {
		if input[i] != '{' {
			out = append(out, input[i])
			i++
			continue
		}
		end := strings.IndexByte(input[i:], '}')
		if end <= 1 {
			return nil, fmt.Errorf("unterminated control token at %d", i)
		}
		token := input[i+1 : i+end]
		seq, ok := controlTokenBytes(token)
		if !ok {
			return nil, fmt.Errorf("unknown control token {%s}", token)
		}
		out = append(out, seq...)
		i += end + 1
	}
	return out, nil
}

// QuoteLiteral encodes text without turning its braces into control markers.
func QuoteLiteral(text string) string {
	return strings.ReplaceAll(text, "{", "{LeftBrace}")
}

func controlTokenBytes(token string) ([]byte, bool) {
	switch token {
	case "LeftBrace":
		return []byte{'{'}, true
	case "RightBrace":
		return []byte{'}'}, true
	case "Enter":
		return []byte{'\r'}, true
	case "Tab":
		return []byte{'\t'}, true
	case "Esc":
		return []byte{0x1b}, true
	case "Backspace":
		return []byte{0x7f}, true
	case "Delete":
		return []byte{0x1b, '[', '3', '~'}, true
	case "Up":
		return []byte{0x1b, '[', 'A'}, true
	case "Down":
		return []byte{0x1b, '[', 'B'}, true
	case "Right":
		return []byte{0x1b, '[', 'C'}, true
	case "Left":
		return []byte{0x1b, '[', 'D'}, true
	case "Home":
		return []byte{0x1b, '[', 'H'}, true
	case "End":
		return []byte{0x1b, '[', 'F'}, true
	case "PageUp":
		return []byte{0x1b, '[', '5', '~'}, true
	case "PageDown":
		return []byte{0x1b, '[', '6', '~'}, true
	}
	if strings.HasPrefix(token, "Ctrl-") && len(token) == 6 {
		letter := token[5]
		if letter >= 'A' && letter <= 'Z' {
			return []byte{letter - 'A' + 1}, true
		}
		if letter >= 'a' && letter <= 'z' {
			return []byte{letter - 'a' + 1}, true
		}
	}
	return nil, false
}
