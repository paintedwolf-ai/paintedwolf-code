package wirespec

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type SecurityRequirement map[string][]string

type securityScheme struct {
	Type   string `yaml:"type"`
	Scheme string `yaml:"scheme"`
}

type openAPIOperationDoc struct {
	OperationID string                 `yaml:"operationId"`
	Security    *[]SecurityRequirement `yaml:"security"`
}

type openAPIDoc struct {
	Security   []SecurityRequirement                     `yaml:"security"`
	Paths      map[string]map[string]openAPIOperationDoc `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]securityScheme `yaml:"securitySchemes"`
	} `yaml:"components"`
}

func LoadOpenAPIDoc(repoRoot string) (*openAPIDoc, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
