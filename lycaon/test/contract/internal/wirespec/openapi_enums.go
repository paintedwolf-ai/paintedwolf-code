package wirespec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type openAPISpec struct {
	Components struct {
		Schemas map[string]OpenAPISchema `yaml:"schemas"`
	} `yaml:"components"`
}

type OpenAPISchema struct {
	Type string   `yaml:"type"`
	Enum []string `yaml:"enum"`
}

func LoadOpenAPIEnums(repoRoot string) (map[string][]string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for name, schema := range spec.Components.Schemas {
		if schema.Type == "string" && len(schema.Enum) > 0 {
			out[name] = append([]string(nil), schema.Enum...)
		}
	}
	return out, nil
}

func ParseTSEnumUnions(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	out := make(map[string][]string)
	// Generated nested enums only. `*` admits single-value enums, which
	// openapi-typescript emits as one bare string literal.
	nested := regexp.MustCompile(`(?m)^        (\w+):\s("(?:[^"]+)"(?:\s*\|\s*"(?:[^"]+)")*);`)
	for _, m := range nested.FindAllStringSubmatch(content, -1) {
		name := m[1]
		var values []string
		for _, seg := range strings.Split(m[2], "|") {
			seg = strings.TrimSpace(seg)
			seg = strings.Trim(seg, `"`)
			if seg != "" {
				values = append(values, seg)
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	return out, nil
}
