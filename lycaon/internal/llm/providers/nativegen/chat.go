package nativegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	gemmaQuote     = `<|"|>`
	turnStart      = "<|turn>"
	turnEnd        = "<turn|>"
	toolStart      = "<|tool>"
	toolEnd        = "<tool|>"
	toolCallStart  = "<|tool_call>"
	toolCallEnd    = "<tool_call|>"
	toolRespStart  = "<|tool_response>"
	toolRespEnd    = "<tool_response|>"
	thoughtChannel = "<|channel>thought\n"
	channelEnd     = "<channel|>"
)

// RenderPrompt serializes a CompletionRequest into the Gemma 4 chat template format.
// By default thinking is disabled (enable_thinking = false), omitting <|think|>.
func RenderPrompt(req modelcall.CompletionRequest, enableThinking bool) (string, error) {
	var b strings.Builder

	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == api.MessageRoleSystem && m.Content != "" {
			systemParts = append(systemParts, m.Content)
		}
	}

	if enableThinking || len(systemParts) > 0 || len(req.Tools) > 0 {
		b.WriteString(turnStart)
		b.WriteString("system\n")
		if enableThinking {
			b.WriteString("<|think|>\n")
		}
		if len(systemParts) > 0 {
			b.WriteString(strings.Join(systemParts, "\n\n"))
		}
		if len(req.Tools) > 0 && req.ToolUse != modelcall.ToolUseForbidden {
			if len(systemParts) > 0 {
				b.WriteString("\n")
			}
			for _, t := range req.Tools {
				decl, err := renderToolDeclaration(t)
				if err != nil {
					return "", err
				}
				b.WriteString(toolStart)
				b.WriteString(decl)
				b.WriteString(toolEnd)
			}
		}
		b.WriteString(turnEnd)
		b.WriteString("\n")
	}

	for _, m := range req.Messages {
		switch m.Role {
		case api.MessageRoleSystem:
			continue
		case api.MessageRoleUser:
			b.WriteString(turnStart)
			b.WriteString("user\n")
			b.WriteString(m.Content)
			b.WriteString(turnEnd)
			b.WriteString("\n")
		case api.MessageRoleAssistant:
			b.WriteString(turnStart)
			b.WriteString("model\n")
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					callStr := RenderToolCall(tc.Name, tc.Args)
					b.WriteString(toolCallStart)
					b.WriteString(callStr)
					b.WriteString(toolCallEnd)
				}
			}
			if m.Content != "" {
				b.WriteString(m.Content)
			}
			b.WriteString(turnEnd)
			b.WriteString("\n")
		case api.MessageRoleTool:
			b.WriteString(turnStart)
			b.WriteString("user\n")
			b.WriteString(toolRespStart)
			name := ""
			content := m.Content
			if m.ToolResult != nil {
				name = m.ToolResult.Tool
				if content == "" {
					content = m.ToolResult.Content
				}
			}
			if name == "" {
				name = "tool"
			}
			b.WriteString("response:")
			b.WriteString(name)
			b.WriteString("{result:")
			b.WriteString(quoteGemmaString(content))
			b.WriteString("}")
			b.WriteString(toolRespEnd)
			b.WriteString(turnEnd)
			b.WriteString("\n")
		}
	}

	b.WriteString(turnStart)
	b.WriteString("model\n")
	return b.String(), nil
}

func renderToolDeclaration(t tools.ToolMeta) (string, error) {
	var b strings.Builder
	b.WriteString("declaration:")
	b.WriteString(t.Name)
	b.WriteString("{description:")
	b.WriteString(quoteGemmaString(t.Description))
	if len(t.ArgsSchema) > 0 {
		b.WriteString(",parameters:")
		b.WriteString(renderGemmaSchemaMap(t.ArgsSchema))
	}
	b.WriteString("}")
	return b.String(), nil
}

func renderGemmaSchemaMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := m[k]
		if k == "type" {
			if s, ok := v.(string); ok {
				parts = append(parts, fmt.Sprintf("type:%s", strings.ToUpper(s)))
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("%s:%s", k, renderGemmaSchemaValue(v)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func renderGemmaSchemaValue(v any) string {
	switch val := v.(type) {
	case map[string]any:
		return renderGemmaSchemaMap(val)
	case []any:
		items := make([]string, 0, len(val))
		for _, item := range val {
			items = append(items, renderGemmaSchemaValue(item))
		}
		return "[" + strings.Join(items, ",") + "]"
	case string:
		return quoteGemmaString(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case nil:
		return "null"
	default:
		return quoteGemmaString(fmt.Sprint(val))
	}
}

// RenderToolCall encodes a tool call into Gemma 4's `call:NAME{key:value,...}` syntax with sorted keys.
func RenderToolCall(name string, args map[string]any) string {
	return "call:" + name + renderGemmaValueMap(args)
}

func renderGemmaValueMap(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+renderGemmaValue(m[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func renderGemmaValue(v any) string {
	switch val := v.(type) {
	case map[string]any:
		return renderGemmaValueMap(val)
	case []any:
		items := make([]string, 0, len(val))
		for _, item := range val {
			items = append(items, renderGemmaValue(item))
		}
		return "[" + strings.Join(items, ",") + "]"
	case string:
		return quoteGemmaString(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case nil:
		return "null"
	default:
		return quoteGemmaString(fmt.Sprint(val))
	}
}

func quoteGemmaString(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, gemmaQuote, `\<|"|>`)
	return gemmaQuote + escaped + gemmaQuote
}

// ParseModelOutput separates visible assistant text, optional thought channels, and completed Gemma 4 tool calls.
// Incomplete `<|tool_call>` blocks (for example on max_tokens termination) are discarded rather than executed.
func ParseModelOutput(raw string) (content string, reasoning string, calls []api.ToolCall, err error) {
	rest := raw

	for {
		start := strings.Index(rest, thoughtChannel)
		if start < 0 {
			break
		}
		afterStart := rest[start+len(thoughtChannel):]
		end := strings.Index(afterStart, channelEnd)
		if end < 0 {
			reasoning += afterStart
			rest = rest[:start]
			break
		}
		if reasoning != "" {
			reasoning += "\n"
		}
		reasoning += afterStart[:end]
		rest = rest[:start] + afterStart[end+len(channelEnd):]
	}

	var textBuilder strings.Builder
	callIndex := 0
	for len(rest) > 0 {
		start := strings.Index(rest, toolCallStart)
		if start < 0 {
			textBuilder.WriteString(rest)
			break
		}
		textBuilder.WriteString(rest[:start])
		afterStart := rest[start+len(toolCallStart):]
		end := strings.Index(afterStart, toolCallEnd)
		if end < 0 {
			// Incomplete tool call at stream end (e.g. length limit): discard without fabricating delimiters.
			break
		}
		callBody := strings.TrimSpace(afterStart[:end])
		rest = afterStart[end+len(toolCallEnd):]

		tc, parseErr := ParseGemmaToolCall(callBody, callIndex)
		if parseErr != nil {
			return "", "", nil, parseErr
		}
		calls = append(calls, tc)
		callIndex++
	}

	cleaned := textBuilder.String()
	cleaned = strings.ReplaceAll(cleaned, turnStart, "")
	cleaned = strings.ReplaceAll(cleaned, turnEnd, "")
	return strings.TrimSpace(cleaned), strings.TrimSpace(reasoning), calls, nil
}

// ParseGemmaToolCall parses `call:NAME{key:value,...}` into an api.ToolCall with JSON arguments.
func ParseGemmaToolCall(body string, index int) (api.ToolCall, error) {
	if !strings.HasPrefix(body, "call:") {
		return api.ToolCall{}, fmt.Errorf("nativegen: malformed tool call prefix in %q", body)
	}
	rem := strings.TrimPrefix(body, "call:")
	braceIdx := strings.IndexByte(rem, '{')
	if braceIdx <= 0 || !strings.HasSuffix(rem, "}") {
		return api.ToolCall{}, fmt.Errorf("nativegen: malformed tool call body %q", body)
	}
	name := strings.TrimSpace(rem[:braceIdx])
	p := &gemmaParser{s: rem[braceIdx:]}
	obj, err := p.parseMap()
	if err != nil {
		return api.ToolCall{}, fmt.Errorf("nativegen: tool call %s: %w", name, err)
	}
	p.skipSpaces()
	if p.pos != len(p.s) {
		return api.ToolCall{}, fmt.Errorf("nativegen: trailing characters in tool call %s", name)
	}
	return api.ToolCall{
		ID:   fmt.Sprintf("call_%d", index+1),
		Name: name,
		Args: obj,
	}, nil
}

type gemmaParser struct {
	s   string
	pos int
}

func (p *gemmaParser) skipSpaces() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *gemmaParser) parseMap() (map[string]any, error) {
	p.skipSpaces()
	if p.pos >= len(p.s) || p.s[p.pos] != '{' {
		return nil, fmt.Errorf("expected '{' at pos %d", p.pos)
	}
	p.pos++
	out := make(map[string]any)
	p.skipSpaces()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
		return out, nil
	}
	for {
		p.skipSpaces()
		key, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		p.skipSpaces()
		if p.pos >= len(p.s) || p.s[p.pos] != ':' {
			return nil, fmt.Errorf("expected ':' after key %q at pos %d", key, p.pos)
		}
		p.pos++
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out[key] = val
		p.skipSpaces()
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("unterminated map")
		}
		if p.s[p.pos] == '}' {
			p.pos++
			return out, nil
		}
		if p.s[p.pos] != ',' {
			return nil, fmt.Errorf("expected ',' or '}' at pos %d", p.pos)
		}
		p.pos++
	}
}

func (p *gemmaParser) parseList() ([]any, error) {
	p.skipSpaces()
	if p.pos >= len(p.s) || p.s[p.pos] != '[' {
		return nil, fmt.Errorf("expected '[' at pos %d", p.pos)
	}
	p.pos++
	out := []any{}
	p.skipSpaces()
	if p.pos < len(p.s) && p.s[p.pos] == ']' {
		p.pos++
		return out, nil
	}
	for {
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, val)
		p.skipSpaces()
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("unterminated list")
		}
		if p.s[p.pos] == ']' {
			p.pos++
			return out, nil
		}
		if p.s[p.pos] != ',' {
			return nil, fmt.Errorf("expected ',' or ']' at pos %d", p.pos)
		}
		p.pos++
	}
}

func (p *gemmaParser) parseValue() (any, error) {
	p.skipSpaces()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("unexpected end of input")
	}
	if strings.HasPrefix(p.s[p.pos:], gemmaQuote) {
		return p.parseQuotedString()
	}
	switch p.s[p.pos] {
	case '{':
		return p.parseMap()
	case '[':
		return p.parseList()
	}
	if strings.HasPrefix(p.s[p.pos:], "true") {
		p.pos += 4
		return true, nil
	}
	if strings.HasPrefix(p.s[p.pos:], "false") {
		p.pos += 5
		return false, nil
	}
	if strings.HasPrefix(p.s[p.pos:], "null") {
		p.pos += 4
		return nil, nil
	}
	return p.parseNumber()
}

func (p *gemmaParser) parseQuotedString() (string, error) {
	p.pos += len(gemmaQuote)
	var b strings.Builder
	for p.pos < len(p.s) {
		if strings.HasPrefix(p.s[p.pos:], `\<|"|>`) {
			b.WriteString(gemmaQuote)
			p.pos += len(`\<|"|>`)
			continue
		}
		if strings.HasPrefix(p.s[p.pos:], `\\`) {
			b.WriteByte('\\')
			p.pos += 2
			continue
		}
		if strings.HasPrefix(p.s[p.pos:], gemmaQuote) {
			p.pos += len(gemmaQuote)
			return b.String(), nil
		}
		b.WriteByte(p.s[p.pos])
		p.pos++
	}
	return "", fmt.Errorf("unterminated <|\\\"|> string")
}

func (p *gemmaParser) parseIdent() (string, error) {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			p.pos++
		} else {
			break
		}
	}
	if p.pos == start {
		return "", fmt.Errorf("expected identifier at pos %d", p.pos)
	}
	return p.s[start:p.pos], nil
}

func (p *gemmaParser) parseNumber() (any, error) {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			p.pos++
		} else {
			break
		}
	}
	raw := p.s[start:p.pos]
	if raw == "" {
		return nil, fmt.Errorf("expected value at pos %d", p.pos)
	}
	if !strings.ContainsAny(raw, ".eE") {
		if iv, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return iv, nil
		}
	}
	fv, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q: %w", raw, err)
	}
	return fv, nil
}
