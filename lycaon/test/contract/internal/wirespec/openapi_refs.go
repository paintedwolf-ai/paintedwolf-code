package wirespec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

var schemaRefRE = regexp.MustCompile(`#/components/schemas/([A-Za-z0-9_]+)`)

func loadOpenAPISchemaNames(repoRoot string) (map[string]struct{}, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		out[name] = struct{}{}
	}
	return out, nil
}

func extractSchemaRefs(content string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, m := range schemaRefRE.FindAllStringSubmatch(content, -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

func ValidateOpenAPISchemaRefs(repoRoot string) error {
	schemas, err := loadOpenAPISchemaNames(repoRoot)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return err
	}
	for ref := range extractSchemaRefs(string(data)) {
		if _, ok := schemas[ref]; !ok {
			return fmt.Errorf("missing components.schemas.%s", ref)
		}
	}
	return nil
}
