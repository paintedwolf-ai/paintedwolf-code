package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	"gopkg.in/yaml.v3"
)

// CompileGateRules preserves rule semantics and rejects ambiguous selections.
// Explicit files prevent a vendor refresh from silently activating new rules.
func CompileGateRules(cfg *OpengrepGatesConfig, moduleRoot string) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("opengrep gates config required")
	}
	paths := append(ActiveVendorGatePaths(cfg), ActiveLycaonGatePaths(cfg)...)
	files := make([]GateRuleFile, 0, len(paths))
	for _, path := range paths {
		raw, err := readGateFile(path, moduleRoot)
		if err != nil {
			return nil, err
		}
		files = append(files, GateRuleFile{Name: path, Data: raw})
	}
	return compiledGates.compile(files)
}

// GateRuleFile binds a diagnostic name to the bytes selected for one rule file.
type GateRuleFile struct {
	Name string
	Data []byte
}

// CompileGateRuleFiles applies the same selection checks to shipped and candidate rules.
func CompileGateRuleFiles(files []GateRuleFile) ([]byte, error) {
	var output struct {
		Rules []yaml.Node `yaml:"rules"`
	}
	seen := make(map[string]string)
	for index, input := range files {
		path := input.Name
		file, err := decodeGateRuleFile(input)
		if err != nil {
			return nil, err
		}
		if len(file.Rules) == 0 {
			return nil, fmt.Errorf("gate %s contains no rules", path)
		}
		for _, node := range file.Rules {
			namespaceAnchors(&node, fmt.Sprintf("selected%d_", index))
			var rule struct {
				ID        string   `yaml:"id"`
				Languages []string `yaml:"languages"`
				Severity  string   `yaml:"severity"`
				Mode      string   `yaml:"mode"`
				Metadata  struct {
					Purpose string `yaml:"purpose"`
				} `yaml:"metadata"`
			}
			if err := node.Decode(&rule); err != nil {
				return nil, fmt.Errorf("gate %s: %w", path, err)
			}
			if rule.ID == "" || len(rule.Languages) == 0 {
				return nil, fmt.Errorf("gate %s: rule requires id and languages", path)
			}
			if rule.Mode != "" && rule.Mode != "search" && rule.Mode != "taint" {
				return nil, fmt.Errorf("gate %s: unsupported mode %q for %s", path, rule.Mode, rule.ID)
			}
			if rule.Metadata.Purpose == "coverage" {
				if rule.Severity != "INFO" {
					return nil, fmt.Errorf("coverage observation %s must be INFO", rule.ID)
				}
			} else if rule.Severity != "ERROR" && rule.Severity != "WARNING" {
				return nil, fmt.Errorf("security rule %s must be ERROR or WARNING", rule.ID)
			}
			if prior, ok := seen[rule.ID]; ok {
				return nil, fmt.Errorf("duplicate gate rule %s in %s and %s", rule.ID, prior, path)
			}
			seen[rule.ID] = path
			output.Rules = append(output.Rules, node)
		}
	}
	if len(output.Rules) == 0 {
		return nil, fmt.Errorf("opengrep selection contains no rules")
	}
	return yaml.Marshal(output)
}

type gateRuleDocument struct {
	Rules []yaml.Node `yaml:"rules"`
}

func decodeGateRuleFile(file GateRuleFile) (gateRuleDocument, error) {
	var document gateRuleDocument
	decoder := yaml.NewDecoder(bytes.NewReader(file.Data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return document, fmt.Errorf("gate %s: %w", file.Name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return document, fmt.Errorf("gate %s must contain exactly one YAML document", file.Name)
	}
	return document, nil
}

func readGateFile(path, moduleRoot string) ([]byte, error) {
	if !filepath.IsLocal(path) || !strings.HasPrefix(path, "config/") || !isGateRuleYAML(path) {
		return nil, fmt.Errorf("gate selection must name a YAML file under config/: %s", path)
	}
	if configlayout.IsModuleRoot(moduleRoot) {
		data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("gate %s: %w", path, err)
		}
		return data, nil
	}
	return config.Read(config.Rel(strings.TrimPrefix(path, "config/")))
}

func namespaceAnchors(node *yaml.Node, prefix string) {
	if node.Anchor != "" {
		node.Anchor = prefix + node.Anchor
	}
	if node.Kind == yaml.AliasNode {
		node.Value = prefix + node.Value
	}
	for _, child := range node.Content {
		namespaceAnchors(child, prefix)
	}
}
