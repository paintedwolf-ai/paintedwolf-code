package fileoutline

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxConfigNestedKeys = 48

// enrichConfigOutline qualifies nested configuration keys as parent.child.
func enrichConfigOutline(res *Result, text string) {
	switch res.Language {
	case "json", "json5":
		enrichJSONOutline(res, text)
	case "yaml":
		enrichYAMLOutline(res, text)
	case "toml":
		enrichTOMLOutline(res, text)
	}
}

func enrichJSONOutline(res *Result, text string) {
	var root any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return
	}
	lines := strings.Split(text, "\n")
	addConfigKeys(res, obj, lines, true)
}

func enrichYAMLOutline(res *Result, text string) {
	var root any
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return
	}
	lines := strings.Split(text, "\n")
	addConfigKeys(res, obj, lines, true)
}

func enrichTOMLOutline(res *Result, text string) {
	// Section headers qualify nested keys alongside the parser's bare keys.
	lines := strings.Split(text, "\n")
	section := ""
	seen := symbolNameSet(res.Symbols)
	var extra []Symbol
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "["), "]"))
			continue
		}
		if section == "" || !strings.Contains(t, "=") {
			continue
		}
		key := strings.TrimSpace(strings.SplitN(t, "=", 2)[0])
		if key == "" || strings.HasPrefix(key, "#") {
			continue
		}
		name := section + "." + key
		if seen[name] {
			continue
		}
		seen[name] = true
		extra = append(extra, Symbol{Kind: "field", Name: name, Line: i + 1})
		if len(extra) >= maxConfigNestedKeys {
			break
		}
	}
	if len(extra) > 0 {
		res.Symbols = appendConfigSymbols(res.Symbols, extra)
	}
}

func addConfigKeys(res *Result, obj map[string]any, lines []string, nested bool) {
	seen := symbolNameSet(res.Symbols)
	var extra []Symbol
	for key, val := range obj {
		line := findConfigKeyLine(lines, key)
		if line <= 0 {
			line = 1
		}
		if !seen[key] {
			seen[key] = true
			extra = append(extra, Symbol{Kind: "field", Name: key, Line: line})
		}
		if !nested {
			continue
		}
		child, ok := val.(map[string]any)
		if !ok {
			continue
		}
		for childKey := range child {
			name := key + "." + childKey
			if seen[name] {
				continue
			}
			seen[name] = true
			childLine := findConfigKeyLine(lines, childKey)
			if childLine <= 0 {
				childLine = line
			}
			extra = append(extra, Symbol{Kind: "field", Name: name, Line: childLine})
			if len(extra) >= maxConfigNestedKeys {
				res.Symbols = appendConfigSymbols(res.Symbols, extra)
				return
			}
		}
	}
	if len(extra) > 0 {
		res.Symbols = appendConfigSymbols(res.Symbols, extra)
	}
}

// appendConfigSymbols adds qualified config keys even when they share a line
// with an existing bare key (mergeSymbols would drop them by line).
func appendConfigSymbols(primary, extra []Symbol) []Symbol {
	seen := symbolNameSet(primary)
	out := append([]Symbol(nil), primary...)
	for _, s := range extra {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func symbolNameSet(symbols []Symbol) map[string]bool {
	out := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		out[s.Name] = true
	}
	return out
}

func findConfigKeyLine(lines []string, key string) int {
	// Match JSON "key", YAML key:, and TOML key = forms.
	jsonNeedle := `"` + key + `"`
	yamlNeedle := key + ":"
	tomlNeedle := key + " ="
	tomlNeedle2 := key + "="
	for i, line := range lines {
		if strings.Contains(line, jsonNeedle) || strings.Contains(line, yamlNeedle) ||
			strings.Contains(line, tomlNeedle) || strings.Contains(line, tomlNeedle2) {
			return i + 1
		}
	}
	return 0
}

func isConfigLanguage(lang, path string) bool {
	switch lang {
	case "json", "json5", "yaml", "toml":
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".yaml", ".yml", ".toml":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	return base == "package.json" || base == "tsconfig.json" || base == "cargo.toml"
}
