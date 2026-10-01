package secretmint

import (
	"encoding/json"
	"strings"
)

// Content scans have fixed byte and line limits.
const (
	fileScanByteCap = 512 * 1024
	fileScanLineCap = 8192
)

func (i *Inspector) inspectFileText(text string) []Candidate {
	if i == nil || text == "" {
		return nil
	}
	if len(text) > fileScanByteCap {
		text = text[:fileScanByteCap]
	}
	var object any
	if strings.Count(text, "\n") < fileScanLineCap && json.Unmarshal([]byte(text), &object) == nil {
		return i.inspectStructuredKeys(object, i.jsonKeys)
	}
	var hits []Candidate
	n := 0
	for _, line := range strings.Split(text, "\n") {
		n++
		if n > fileScanLineCap {
			break
		}
		key, val, ok := parseFileAssignmentLine(line)
		if !ok {
			continue
		}
		canon, ok := i.credentialSlot(key, i.fileKeys)
		if !ok {
			continue
		}
		hits = append(hits, i.candidate(canon, val)...)
	}
	return hits
}

func parseFileAssignmentLine(line string) (key, val string, ok bool) {
	s := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
		return "", "", false
	}
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "-\t") {
		s = strings.TrimSpace(s[1:])
	}
	if strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "{") {
		if key, val, ok = parseJSONMember(s); ok {
			return key, val, true
		}
	}
	if key, val, ok = parseEnvLine(s); ok {
		return key, val, true
	}
	return parseYAMLScalar(s)
}

func parseEnvLine(s string) (key, val string, ok bool) {
	key, val, ok = splitEnvAssignment(s)
	if !ok {
		return "", "", false
	}
	if !quotedScalar(val) {
		val = stripTrailingHashComment(val)
	}
	return key, val, true
}

func parseYAMLScalar(line string) (key, val string, ok bool) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:colon])
	if !envName(key) {
		return "", "", false
	}
	rest := strings.TrimSpace(line[colon+1:])
	if rest == "" || isYAMLBlockIndicator(rest) {
		return "", "", false
	}
	if strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "[") {
		return "", "", false
	}
	val = rest
	if quotedScalar(val) {
		inner, _, qok := takeQuoted(val)
		if qok {
			val = inner
		}
	} else {
		val = stripTrailingHashComment(val)
	}
	return key, val, true
}

func isYAMLBlockIndicator(rest string) bool {
	tok, _, _ := strings.Cut(rest, " ")
	tok, _, _ = strings.Cut(tok, "\t")
	tok, _, _ = strings.Cut(tok, "#")
	switch tok {
	case "|", ">", "|-", ">-", "|+", ">+":
		return true
	default:
		return false
	}
}

func parseJSONMember(s string) (key, val string, ok bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		s = strings.TrimSpace(s[1:])
	}
	if !strings.HasPrefix(s, `"`) && !strings.HasPrefix(s, `'`) {
		return "", "", false
	}
	key, rest, ok := takeQuoted(s)
	if !ok || key == "" {
		return "", "", false
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, ":") {
		return "", "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if quotedScalar(rest) {
		val, _, ok = takeQuoted(rest)
		if !ok {
			return "", "", false
		}
		return key, val, true
	}
	end := len(rest)
	if i := strings.IndexAny(rest, ",}"); i >= 0 {
		end = i
	}
	val = strings.TrimSpace(rest[:end])
	if val == "" {
		return "", "", false
	}
	return key, val, true
}

func takeQuoted(s string) (inner, rest string, ok bool) {
	if len(s) < 2 {
		return "", "", false
	}
	q := s[0]
	if q != '"' && q != '\'' {
		return "", "", false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == q {
			return s[1:i], s[i+1:], true
		}
	}
	return "", "", false
}

func quotedScalar(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 2 && ((s[0] == '"' || s[0] == '\'') && strings.IndexByte(s[1:], s[0]) >= 0)
}

func stripTrailingHashComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, "\t#"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
