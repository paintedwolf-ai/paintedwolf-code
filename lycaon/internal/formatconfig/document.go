package formatconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

type format int

const (
	// formatScript is executable configuration the host never runs; finding it
	// ends the tool's search with nothing declared.
	formatScript format = iota
	formatJSON
	formatJSONC
	formatYAML
	// formatYAMLDocuments keeps the first YAML document that names no language.
	formatYAMLDocuments
	formatTOML
	formatINI
	// formatKeyValue is one `key = value` or `key: value` pair per line.
	formatKeyValue
)

// document is a decoded configuration tree.
type document map[string]any

// decode reads a configuration file. It reports false when the file is not
// readable: executable configuration, or text its tool would reject too.
func decode(f format, body []byte) (document, bool) {
	var (
		doc document
		err error
	)
	switch f {
	case formatScript:
		return nil, false
	case formatJSON:
		doc, err = decodeJSON(body)
	case formatJSONC:
		doc, err = decodeJSON(stripJSONC(body))
	case formatYAML:
		err = yaml.Unmarshal(body, &doc)
	case formatYAMLDocuments:
		doc, err = decodeYAMLDocuments(body)
	case formatTOML:
		_, err = toml.Decode(string(body), &doc)
	case formatINI:
		doc = decodeINI(body)
	case formatKeyValue:
		doc = decodeKeyValue(body)
	}
	if err != nil {
		return nil, false
	}
	return doc, true
}

func decodeJSON(body []byte) (document, error) {
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func decodeYAMLDocuments(body []byte) (document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	var first document
	for {
		var doc document
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		if _, named := doc["Language"]; !named {
			return doc, nil
		}
		if first == nil {
			first = doc
		}
	}
	return first, nil
}

// stripJSONC removes comments and trailing commas outside strings.
func stripJSONC(body []byte) []byte {
	out := make([]byte, 0, len(body))
	inString, escaped := false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(body) && body[i+1] == '/':
			for i < len(body) && body[i] != '\n' {
				i++
			}
			if i < len(body) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			i += 2
			for i+1 < len(body) && (body[i] != '*' || body[i+1] != '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			trimmed := bytes.TrimRight(out, " \t\r\n")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ',' {
				out = append(trimmed[:len(trimmed)-1], out[len(trimmed):]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// decodeINI keeps `[section]` tables of `key = value` pairs; the preamble is the root.
func decodeINI(body []byte) document {
	doc := document{}
	table := doc
	for _, line := range configLines(body) {
		if len(line) > 2 && line[0] == '[' && line[len(line)-1] == ']' {
			table = document{}
			doc[strings.TrimSpace(line[1:len(line)-1])] = table
			continue
		}
		if key, value, ok := cutPair(line, "="); ok {
			table[key] = value
		}
	}
	return doc
}

func decodeKeyValue(body []byte) document {
	doc := document{}
	for _, line := range configLines(body) {
		if key, value, ok := cutPair(line, "="); ok {
			doc[key] = value
		} else if key, value, ok := cutPair(line, ":"); ok {
			doc[key] = value
		}
	}
	return doc
}

func configLines(body []byte) []string {
	var lines []string
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func cutPair(line, sep string) (string, string, bool) {
	key, value, ok := strings.Cut(line, sep)
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if !ok || key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.Trim(value, `"'`), true
}

// at follows a path of keys through nested tables.
func (d document) at(path ...string) (any, bool) {
	var current any = map[string]any(d)
	for _, key := range path {
		table, ok := asTable(current)
		if !ok {
			return nil, false
		}
		current, ok = table[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func (d document) table(path ...string) (document, bool) {
	v, ok := d.at(path...)
	if !ok {
		return nil, false
	}
	t, ok := asTable(v)
	return document(t), ok
}

func asTable(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case document:
		return t, true
	}
	return nil, false
}

// int reads a whole number given as a number or a numeric string.
func (d document) int(path ...string) (int, bool) {
	v, ok := d.at(path...)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		if n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case float64:
		if n != math.Trunc(n) || n < math.MinInt32 || n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

func (d document) str(path ...string) (string, bool) {
	v, ok := d.at(path...)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (d document) bool(path ...string) (bool, bool) {
	v, ok := d.at(path...)
	if !ok {
		return false, false
	}
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		return parsed, err == nil
	}
	return false, false
}
