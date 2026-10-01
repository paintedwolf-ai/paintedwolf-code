package sourcescope

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ProjectScopeRelPath is the project overlay file, relative to the root.
func ProjectScopeRelPath() string { return settingsoverlay.Rel("source-scope.yaml") }

// ProjectScopePath returns {projectDir}/<overlay>/source-scope.yaml.
func ProjectScopePath(projectDir string) string {
	return filepath.Join(projectDir, ProjectScopeRelPath())
}

// Declared holds root-relative capture rules and per-plane budget overrides.
// Includes override ignore rules; explicit exclusions and budgets still apply.
type Declared struct {
	Include []string
	Exclude []string
	Capture *sandbox.SurveyBudgets
	Catalog *sandbox.SurveyBudgets
}

// Empty reports whether the project declared nothing.
func (d Declared) Empty() bool {
	return len(d.Include) == 0 && len(d.Exclude) == 0 && d.Capture == nil && d.Catalog == nil
}

type declaredYAML struct {
	Version int      `yaml:"version"`
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
	Budgets struct {
		Capture *budgetsYAML `yaml:"capture"`
		Catalog *budgetsYAML `yaml:"catalog"`
	} `yaml:"budgets"`
}

// LoadDeclared accepts a missing overlay and rejects unreadable or invalid files.
func LoadDeclared(projectDir string) (Declared, error) {
	path := ProjectScopePath(projectDir)
	data, err := os.ReadFile(path) // #nosec G304 -- project overlay under the caller's project dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Declared{}, nil
		}
		return Declared{}, fmt.Errorf("read %s: %w", ProjectScopeRelPath(), err)
	}
	var y declaredYAML
	if err := config.DecodeYAML(data, &y); err != nil {
		return Declared{}, fmt.Errorf("parse %s: %w", ProjectScopeRelPath(), err)
	}
	if y.Version != 0 && y.Version != 1 {
		return Declared{}, fmt.Errorf("%s: version %d is not supported", ProjectScopeRelPath(), y.Version)
	}
	out := Declared{Include: cleanPatterns(y.Include), Exclude: cleanPatterns(y.Exclude)}
	if y.Budgets.Capture != nil {
		b := y.Budgets.Capture.apply(sandbox.SurveyBudgets{})
		out.Capture = &b
	}
	if y.Budgets.Catalog != nil {
		b := y.Budgets.Catalog.apply(sandbox.SurveyBudgets{})
		out.Catalog = &b
	}
	return out, nil
}

func cleanPatterns(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Only positive project budgets override device values.
func applyDeclaredBudgets(base sandbox.SurveyBudgets, declared *sandbox.SurveyBudgets) sandbox.SurveyBudgets {
	if declared == nil {
		return base
	}
	if declared.DirectoryEntries > 0 {
		base.DirectoryEntries = declared.DirectoryEntries
	}
	if declared.SubtreeEntries > 0 {
		base.SubtreeEntries = declared.SubtreeEntries
	}
	if declared.WalkEntries > 0 {
		base.WalkEntries = declared.WalkEntries
	}
	return base
}
