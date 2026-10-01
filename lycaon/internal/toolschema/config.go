package toolschema

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// toolNameStem validates tool names used as schema filenames.
var toolNameStem = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Config is the merged in-memory tool schema catalog (name → Entry).
type Config struct {
	Tools map[string]Entry `yaml:"tools"`
}

// Entry describes model-facing metadata for one tool.
// On disk, unit files place these fields at the document root (no tools: wrapper).
type Entry struct {
	Description string         `yaml:"description"`
	Tags        []string       `yaml:"tags,omitempty"`
	Schema      map[string]any `yaml:"schema"`
}

// Meta is registry-facing tool metadata derived from config.
type Meta struct {
	Name        string
	Description string
	Tags        []string
	ArgsSchema  map[string]any
}

// ValidToolName reports whether name is a legal tools/schemas/<name> stem.
func ValidToolName(name string) bool {
	return toolNameStem.MatchString(strings.TrimSpace(name))
}

// ParseEntry unmarshals a single-tool unit body (Entry at document root).
func ParseEntry(data []byte) (Entry, error) {
	var entry Entry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&entry); err != nil {
		return Entry{}, err
	}
	if strings.TrimSpace(entry.Description) == "" && entry.Schema == nil {
		return Entry{}, fmt.Errorf("empty tool schema entry")
	}
	return entry, nil
}

// ConfigFromEntries builds Config from name → Entry (names already validated).
func ConfigFromEntries(entries map[string]Entry) *Config {
	out := make(map[string]Entry, len(entries))
	for name, entry := range entries {
		out[name] = entry
	}
	return &Config{Tools: out}
}

// LoadSchemaDir walks dir for *.yaml Entry-at-root files (same on-disk shape as pack units).
func LoadSchemaDir(dir string) (*Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Entry)
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		if !ValidToolName(stem) {
			return nil, fmt.Errorf("invalid tool schema stem %q in %s", stem, dir)
		}
		// #nosec G304 -- name is a schema entry returned by ReadDir.
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		entry, err := ParseEntry(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, dup := out[stem]; dup {
			return nil, fmt.Errorf("duplicate tool schema stem %q in %s", stem, dir)
		}
		out[stem] = entry
	}
	return ConfigFromEntries(out), nil
}

// ToolMeta returns metadata for a tool name.
func (c *Config) ToolMeta(name string) (Meta, bool) {
	if c == nil || c.Tools == nil {
		return Meta{}, false
	}
	entry, ok := c.Tools[strings.TrimSpace(name)]
	if !ok {
		return Meta{}, false
	}
	desc := strings.TrimSpace(entry.Description)
	if desc == "" {
		desc = name + " tool"
	}
	schema := entry.Schema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return Meta{
		Name:        name,
		Description: desc,
		Tags:        append([]string(nil), entry.Tags...),
		ArgsSchema:  schema,
	}, true
}

// ArgFieldSummary returns required/optional arg names from a JSON schema object.
func ArgFieldSummary(schema map[string]any) (required, optional []string) {
	if schema == nil {
		return nil, nil
	}
	reqSet := map[string]bool{}
	switch raw := schema["required"].(type) {
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				reqSet[s] = true
			}
		}
	case []string:
		for _, s := range raw {
			reqSet[s] = true
		}
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil, nil
	}
	for name := range props {
		if reqSet[name] {
			required = append(required, name)
		} else {
			optional = append(optional, name)
		}
	}
	sort.Strings(required)
	sort.Strings(optional)
	return required, optional
}

// ArgFieldPaths returns accepted top-level and nested argument paths.
func ArgFieldPaths(schema map[string]any) []string {
	seen := map[string]bool{}
	collectArgFieldPaths(schema, "", seen, 0)
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// argFieldDepthMax bounds the walk; tool schemas nest two or three levels.
const argFieldDepthMax = 6

func collectArgFieldPaths(schema map[string]any, prefix string, out map[string]bool, depth int) {
	if schema == nil || depth > argFieldDepthMax {
		return
	}
	if items, ok := schema["items"].(map[string]any); ok {
		collectArgFieldPaths(items, prefix, out, depth+1)
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		if strings.TrimSpace(name) == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = true
		if child, ok := raw.(map[string]any); ok {
			collectArgFieldPaths(child, path, out, depth+1)
		}
	}
}
