// Package harmony recovers leaked Harmony channel transcripts: role tokens,
// channel names, and `commentary to=functions.<name> json` envelopes.
package harmony

import (
	"encoding/json"
	"strings"
)

const (
	roleAssistant     = "assistant"
	ChannelAnalysis   = "analysis"
	ChannelCommentary = "commentary"
	ChannelFinal      = "final"
	funcPrefix        = " to=functions."
	commentaryCall    = ChannelCommentary + funcPrefix
)

// Segment is one Harmony channel payload recovered from leaked text.
type Segment struct {
	Channel string
	Tool    string
	Args    map[string]any
	Text    string
}

// LooksLikeTranscript reports leaked Harmony channel markers.
func LooksLikeTranscript(content string) bool {
	s := strings.TrimSpace(content)
	if s == "" {
		return false
	}
	if strings.Contains(s, commentaryCall) {
		return true
	}
	if strings.Contains(s, roleAssistant+ChannelCommentary) ||
		strings.Contains(s, roleAssistant+ChannelFinal) ||
		strings.Contains(s, roleAssistant+ChannelAnalysis) {
		return true
	}
	if strings.HasPrefix(s, ChannelFinal+"{") || strings.HasPrefix(s, ChannelFinal+"\n{") {
		return true
	}
	return leadingAssistantCount(s) >= 2
}

// ParseTranscript splits a leaked Harmony body into channel segments.
func ParseTranscript(content string) ([]Segment, bool) {
	if !LooksLikeTranscript(content) {
		return nil, false
	}
	s := PeelRolePrefix(strings.TrimSpace(content))
	if strings.HasPrefix(s, "{") {
		return []Segment{{Channel: ChannelFinal, Text: s}}, true
	}
	var segs []Segment
	for s != "" {
		ch, tool, rest, ok := splitChannelHeader(s)
		if !ok {
			return nil, false
		}
		s = rest
		if ch == ChannelCommentary && tool != "" {
			args, consumed, parsed := firstJSONObject(s)
			if !parsed {
				return nil, false
			}
			segs = append(segs, Segment{Channel: ch, Tool: tool, Args: args})
			s = strings.TrimSpace(s[consumed:])
			continue
		}
		next := nextChannelIndex(s)
		text := s
		if next >= 0 {
			text = s[:next]
			s = s[next:]
		} else {
			s = ""
		}
		segs = append(segs, Segment{Channel: ch, Text: strings.TrimSpace(text)})
	}
	return segs, len(segs) > 0
}

// PeelRolePrefix removes leading Harmony `assistant` role tokens when the next
// rune starts a channel name, another role token, or a JSON object.
func PeelRolePrefix(s string) string {
	for strings.HasPrefix(s, roleAssistant) {
		rest := s[len(roleAssistant):]
		if strings.HasPrefix(rest, roleAssistant) || startsChannel(rest) || strings.HasPrefix(rest, "{") {
			s = rest
			continue
		}
		break
	}
	return s
}

func leadingAssistantCount(s string) int {
	n := 0
	for strings.HasPrefix(s, roleAssistant) {
		n++
		s = s[len(roleAssistant):]
	}
	return n
}

func startsChannel(s string) bool {
	return strings.HasPrefix(s, ChannelAnalysis) || strings.HasPrefix(s, ChannelCommentary) || strings.HasPrefix(s, ChannelFinal)
}

func splitChannelHeader(s string) (channel, tool, rest string, ok bool) {
	s = PeelRolePrefix(s)
	switch {
	case strings.HasPrefix(s, ChannelCommentary):
		body := s[len(ChannelCommentary):]
		if !strings.HasPrefix(body, funcPrefix) {
			return "", "", "", false
		}
		body = body[len(funcPrefix):]
		nameEnd := strings.IndexAny(body, " \t\n\r")
		if nameEnd <= 0 {
			return "", "", "", false
		}
		tool = strings.TrimSpace(body[:nameEnd])
		if tool == "" {
			return "", "", "", false
		}
		body = strings.TrimLeft(body[nameEnd:], " \t")
		if !strings.HasPrefix(body, "json") {
			return "", "", "", false
		}
		return ChannelCommentary, tool, strings.TrimSpace(body[len("json"):]), true
	case strings.HasPrefix(s, ChannelAnalysis):
		return ChannelAnalysis, "", s[len(ChannelAnalysis):], true
	case strings.HasPrefix(s, ChannelFinal):
		return ChannelFinal, "", s[len(ChannelFinal):], true
	default:
		return "", "", "", false
	}
}

func nextChannelIndex(s string) int {
	needles := []string{
		roleAssistant + ChannelCommentary,
		roleAssistant + ChannelAnalysis,
		roleAssistant + ChannelFinal,
		commentaryCall,
	}
	found := -1
	for _, n := range needles {
		if i := strings.Index(s, n); i >= 0 && (found < 0 || i < found) {
			found = i
		}
	}
	return found
}

func firstJSONObject(s string) (map[string]any, int, bool) {
	s = strings.TrimLeft(s, " \t\n\r")
	if !strings.HasPrefix(s, "{") {
		return nil, 0, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, 0, false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, 0, false
	}
	return obj, int(dec.InputOffset()), true
}
