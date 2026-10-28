package secretharvest

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"gopkg.in/yaml.v3"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Pair is one parsed name/value binding.
type Pair struct {
	Name  string
	Value string
}

// ParseContainer extracts bounded JSON, YAML, and key-value bindings.
func ParseContainer(container string, content []byte) []Pair {
	text := string(content)
	var pairs []Pair
	switch {
	case json.Valid(content):
		var doc any
		if err := json.Unmarshal(content, &doc); err == nil {
			pairs = walkDoc("", doc)
			break
		}
		pairs = parseLines(text)
	case looksYAML(container):
		var doc any
		if err := yaml.Unmarshal(content, &doc); err == nil {
			pairs = walkDoc("", doc)
			break
		}
		pairs = parseLines(text)
	default:
		pairs = parseLines(text)
	}
	out := pairs[:0]
	for _, p := range pairs {
		if harvestable(p.Value) {
			out = append(out, p)
		}
		if len(out) >= MaxValuesPerContainer {
			break
		}
	}
	return out
}

func looksYAML(container string) bool {
	lower := strings.ToLower(container)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// parseLines reads KEY=VALUE bindings: dotenv, ini, and properties shapes.
func parseLines(text string) []Pair {
	var out []Pair
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexAny(line, "=:")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		value = strings.Trim(value, `"'`)
		if name == "" || value == "" {
			continue
		}
		out = append(out, Pair{Name: name, Value: value})
	}
	return out
}

func walkDoc(prefix string, doc any) []Pair {
	var out []Pair
	switch node := doc.(type) {
	case map[string]any:
		for key, child := range node {
			out = append(out, walkDoc(joinPath(prefix, key), child)...)
		}
	case map[any]any:
		for key, child := range node {
			out = append(out, walkDoc(joinPath(prefix, toKey(key)), child)...)
		}
	case []any:
		for i, child := range node {
			out = append(out, walkDoc(joinPath(prefix, strconv.Itoa(i)), child)...)
		}
	case string:
		if prefix != "" {
			out = append(out, Pair{Name: prefix, Value: node})
		}
	}
	return out
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func toKey(key any) string {
	if s, ok := key.(string); ok {
		return s
	}
	return ""
}

// Container bindings use the stricter harvested floor.
func harvestable(value string) bool {
	if utf8.RuneCountInString(value) < secretmatch.MinNeedleRunes(false) {
		return false
	}
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return false
	}
	return true
}
