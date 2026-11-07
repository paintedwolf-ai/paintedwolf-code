package nativegen

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

type argumentParser struct {
	text   string
	pos    int
	depth  int
	values int
}

func parseArguments(text string) (map[string]any, error) {
	if len(text) > 1<<20 {
		return nil, errors.New("tool arguments exceed their bound")
	}
	p := argumentParser{text: text}
	value, err := p.value()
	if err != nil {
		return nil, err
	}
	p.spaces()
	object, ok := value.(map[string]any)
	if !ok || p.pos != len(text) {
		return nil, errors.New("tool arguments must be one object")
	}
	return object, nil
}

func (p *argumentParser) spaces() {
	for p.pos < len(p.text) && strings.ContainsRune(" \n\r\t", rune(p.text[p.pos])) {
		p.pos++
	}
}

func (p *argumentParser) take(c byte) bool {
	p.spaces()
	if p.pos < len(p.text) && p.text[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *argumentParser) value() (any, error) {
	p.spaces()
	p.values++
	if p.pos >= len(p.text) || p.depth >= 64 || p.values > 16384 {
		return nil, errors.New("tool arguments are incomplete or exceed their bounds")
	}
	p.depth++
	defer func() { p.depth-- }()
	switch p.text[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	default:
		decoder := json.NewDecoder(strings.NewReader(p.text[p.pos:]))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if number, ok := value.(json.Number); ok {
			if _, err := strconv.ParseFloat(string(number), 64); err != nil {
				return nil, err
			}
		}
		p.pos += int(decoder.InputOffset())
		return value, nil
	}
}

func (p *argumentParser) key() (string, error) {
	p.spaces()
	if p.pos >= len(p.text) {
		return "", io.ErrUnexpectedEOF
	}
	if p.text[p.pos] == '"' {
		value, err := p.value()
		if err != nil {
			return "", err
		}
		key, ok := value.(string)
		if !ok {
			return "", errors.New("tool argument key is not a string")
		}
		return key, nil
	}
	start := p.pos
	for p.pos < len(p.text) && p.text[p.pos] != ':' {
		if strings.ContainsRune("{},[]\"\n\r\t", rune(p.text[p.pos])) {
			return "", errors.New("invalid unquoted tool argument key")
		}
		p.pos++
	}
	key := strings.TrimSpace(p.text[start:p.pos])
	if key == "" {
		return "", errors.New("empty tool argument key")
	}
	return key, nil
}

func (p *argumentParser) object() (map[string]any, error) {
	p.pos++
	result := make(map[string]any)
	if p.take('}') {
		return result, nil
	}
	for {
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists || !p.take(':') {
			return nil, errors.New("duplicate or incomplete tool argument key")
		}
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		result[key] = value
		if p.take('}') {
			return result, nil
		}
		if !p.take(',') {
			return nil, errors.New("missing tool argument separator")
		}
	}
}

func (p *argumentParser) array() ([]any, error) {
	p.pos++
	result := make([]any, 0)
	if p.take(']') {
		return result, nil
	}
	for {
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		if p.take(']') {
			return result, nil
		}
		if !p.take(',') {
			return nil, errors.New("missing tool argument separator")
		}
	}
}
