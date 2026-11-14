package formatconfig

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// decodeXMLProperty scans an XML config for a named element and extracts a
// child <property name="<propName>" value="<N>"/> integer value.
func decodeXMLProperty(body []byte, elemName, attrName, attrValue, propName string) (document, bool) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var stack []bool
	doc := document{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return doc, true
		}
		if err != nil {
			return nil, false
		}
		switch el := tok.(type) {
		case xml.StartElement:
			inTarget := len(stack) > 0 && stack[len(stack)-1]
			if el.Name.Local == elemName {
				inTarget = xmlAttr(el.Attr, attrName) == attrValue
			}
			if inTarget && el.Name.Local == "property" && xmlAttr(el.Attr, "name") == propName {
				if n, err := strconv.Atoi(strings.TrimSpace(xmlAttr(el.Attr, "value"))); err == nil {
					doc[propName] = n
				}
			}
			stack = append(stack, inTarget)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func xmlAttr(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// decodeElixirFormatter scans `.formatter.exs` outside comments and strings
// for top-level `line_length: N`.
func decodeElixirFormatter(body []byte) (document, bool) {
	src := string(body)
	doc := document{}
	var stack []byte
	i := 0
	for i < len(src) {
		ch := src[i]
		switch {
		case ch == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], `"""`) || strings.HasPrefix(src[i:], `'''`):
			quote := src[i : i+3]
			end := strings.Index(src[i+3:], quote)
			if end < 0 {
				return nil, false
			}
			i += 3 + end + 3
		case ch == '"' || ch == '\'':
			next, ok := skipQuoted(src, i, ch)
			if !ok {
				return nil, false
			}
			i = next
		case ch == '[' || ch == '{' || ch == '(':
			stack = append(stack, matchingClose(ch))
			i++
		case ch == ']' || ch == '}' || ch == ')':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return nil, false
			}
			stack = stack[:len(stack)-1]
			i++
		case len(stack) <= 1 && isWordStart(ch):
			start := i
			for i < len(src) && (isWordChar(src[i])) {
				i++
			}
			if src[start:i] == "line_length" && i < len(src) && src[i] == ':' {
				i++
				for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
					i++
				}
				numStart := i
				for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '_') {
					i++
				}
				if numStart < i {
					clean := strings.ReplaceAll(src[numStart:i], "_", "")
					if n, err := strconv.Atoi(clean); err == nil {
						doc["line_length"] = n
					}
				}
			}
		default:
			i++
		}
	}
	if len(stack) != 0 {
		return nil, false
	}
	return doc, true
}

// decodeEDNCljfmt scans a Clojure/EDN map outside `;` comments and strings for
// `:max-line-width` and `:wrap-lines?`.
func decodeEDNCljfmt(body []byte) (document, bool) {
	src := string(body)
	doc := document{}
	var stack []byte
	seenMap := false
	var prevKey string
	i := 0
	for i < len(src) {
		ch := src[i]
		switch {
		case ch == ';':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case unicode.IsSpace(rune(ch)) || ch == ',':
			i++
		case ch == '"':
			next, ok := skipQuoted(src, i, '"')
			if !ok {
				return nil, false
			}
			prevKey = ""
			i = next
		case ch == '{' || ch == '[' || ch == '(':
			if len(stack) == 0 {
				if ch != '{' {
					return nil, false
				}
				seenMap = true
			}
			stack = append(stack, matchingClose(ch))
			prevKey = ""
			i++
		case ch == '}' || ch == ']' || ch == ')':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return nil, false
			}
			stack = stack[:len(stack)-1]
			prevKey = ""
			i++
		default:
			if len(stack) == 0 {
				return nil, false
			}
			start := i
			for i < len(src) && !unicode.IsSpace(rune(src[i])) && !strings.ContainsRune(",;\"{}[]()", rune(src[i])) {
				i++
			}
			tok := src[start:i]
			if len(stack) == 1 {
				switch prevKey {
				case ":max-line-width":
					if n, err := strconv.Atoi(tok); err == nil {
						doc["max-line-width"] = n
					}
					prevKey = ""
				case ":wrap-lines?":
					if tok == "true" {
						doc["wrap-lines?"] = true
					} else if tok == "false" {
						doc["wrap-lines?"] = false
					}
					prevKey = ""
				default:
					if tok == ":max-line-width" || tok == ":wrap-lines?" {
						prevKey = tok
					} else {
						prevKey = ""
					}
				}
			}
		}
	}
	if len(stack) != 0 || !seenMap {
		return nil, false
	}
	return doc, true
}

func skipQuoted(src string, start int, quote byte) (int, bool) {
	i := start + 1
	for i < len(src) {
		if src[i] == '\\' {
			i += 2
			continue
		}
		if src[i] == quote {
			return i + 1, true
		}
		i++
	}
	return 0, false
}

func matchingClose(open byte) byte {
	switch open {
	case '[':
		return ']'
	case '{':
		return '}'
	default:
		return ')'
	}
}

func isWordStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isWordChar(b byte) bool {
	return isWordStart(b) || (b >= '0' && b <= '9')
}
