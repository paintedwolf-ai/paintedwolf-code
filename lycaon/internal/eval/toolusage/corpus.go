package toolusage

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CorpusTask is one isolated live scenario.
type CorpusTask struct {
	ID        string           `yaml:"id" json:"id"`
	Prompt    string           `yaml:"prompt" json:"prompt"`
	FollowUps []CorpusFollowUp `yaml:"follow_ups,omitempty" json:"follow_ups,omitempty"`
}

// CorpusFollowUp continues the same session, optionally after real compaction.
type CorpusFollowUp struct {
	Prompt        string `yaml:"prompt" json:"prompt"`
	CompactBefore bool   `yaml:"compact_before,omitempty" json:"compact_before,omitempty"`
}

// Corpus is the committed set of live scenarios.
type Corpus struct {
	ID    string       `yaml:"id"`
	Tasks []CorpusTask `yaml:"tasks"`
}

// DefaultCorpusPath is relative to the module root.
const DefaultCorpusPath = "test/fixtures/eval/corpus.yaml"

// LoadCorpus reads a corpus YAML file from disk.
func LoadCorpus(path string) (*Corpus, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- caller-resolved corpus path
	if err != nil {
		return nil, err
	}
	var c Corpus
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.ID == "" {
		return nil, fmt.Errorf("corpus %q has no id", path)
	}
	if len(c.Tasks) == 0 {
		return nil, fmt.Errorf("corpus %q has no tasks", path)
	}
	return &c, nil
}

// ResolveModulePath resolves path against the lycaon module root when relative.
func ResolveModulePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, path), nil
}

func moduleRoot() (string, error) {
	if root := os.Getenv("LYCAON_MODULE_ROOT"); root != "" {
		return root, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not locate lycaon module root from %q", cwd)
}
