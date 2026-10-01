package docext

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/docformat"
	"golang.org/x/text/encoding/charmap"
)

func extractRTF(ctx context.Context, req boundedRequest) (Result, error) {
	head := bytes.TrimSpace(req.Bytes)
	if len(head) > 64 {
		head = head[:64]
	}
	if !bytes.Contains(head, []byte(`\rtf`)) {
		return Result{}, attacherr.Unsupported("not a valid RTF document")
	}
	return extractInWorker(ctx, docformat.RTF, req)
}

func extractRTFDirect(req boundedRequest) (Result, error) {
	text := stripRTF(string(req.Bytes))
	return Result{Text: strings.TrimSpace(text), UnitCount: 1}, nil
}

func stripRTF(s string) string {
	var b strings.Builder
	b.Grow(len(s) / 4)
	type state struct {
		skip     bool
		fallback int
		charset  *charmap.Charmap
	}
	current := state{fallback: 1, charset: charmap.Windows1252}
	stack := make([]state, 0, 8)
	skipFallback := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '{':
			stack = append(stack, current)
			i++
		case '}':
			if len(stack) > 0 {
				current = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			i++
		case '\\':
			i++
			if i >= len(s) {
				break
			}
			// Hex escape \'hh
			if s[i] == '\'' && i+2 < len(s) {
				h1, h2 := s[i+1], s[i+2]
				if isHex(h1) && isHex(h2) {
					if skipFallback > 0 {
						skipFallback--
					} else if !current.skip {
						b.WriteRune(current.charset.DecodeByte(fromHex(h1)<<4 | fromHex(h2)))
					}
					i += 3
					continue
				}
			}
			// Control word
			if isAlpha(s[i]) {
				start := i
				for i < len(s) && isAlpha(s[i]) {
					i++
				}
				word := strings.ToLower(s[start:i])
				argStart := i
				if i < len(s) && s[i] == '-' {
					i++
				}
				for i < len(s) && s[i] >= '0' && s[i] <= '9' {
					i++
				}
				arg, hasArg := 0, i > argStart
				if hasArg {
					arg, _ = strconv.Atoi(s[argStart:i])
				}
				if i < len(s) && s[i] == ' ' {
					i++
				}
				switch word {
				case "par", "line", "page":
					if !current.skip {
						b.WriteByte('\n')
					}
				case "tab":
					if !current.skip {
						b.WriteByte('\t')
					}
				case "u":
					if hasArg && !current.skip {
						value := arg
						if value < 0 {
							value += 1 << 16
						}
						b.WriteRune(rune(value))
					}
					skipFallback = current.fallback
				case "uc":
					if hasArg && arg >= 0 {
						current.fallback = arg
					}
				case "ansicpg":
					if hasArg {
						current.charset = rtfCharset(arg)
					}
				case "fonttbl", "colortbl", "stylesheet", "info", "pict", "object", "header", "footer":
					current.skip = true
				}
				continue
			}
			// Escaped special
			if s[i] == '*' {
				current.skip = true
			} else if skipFallback > 0 {
				skipFallback--
			} else if !current.skip {
				switch s[i] {
				case '{', '}', '\\':
					b.WriteByte(s[i])
				case '~':
					b.WriteRune('\u00a0')
				case '_':
					b.WriteRune('\u2011')
				}
			}
			i++
		default:
			if skipFallback > 0 {
				skipFallback--
			} else if !current.skip && c != '\r' {
				if c < 0x80 {
					b.WriteByte(c)
				} else {
					b.WriteRune(current.charset.DecodeByte(c))
				}
			}
			i++
		}
	}
	return b.String()
}

func rtfCharset(codepage int) *charmap.Charmap {
	switch codepage {
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	case 1255:
		return charmap.Windows1255
	case 1256:
		return charmap.Windows1256
	case 1257:
		return charmap.Windows1257
	case 1258:
		return charmap.Windows1258
	default:
		return charmap.Windows1252
	}
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func fromHex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
