// Package sourcescope defines capture admission, catalog traversal priority
// and default disclosure, and per-plane observation budgets.
package sourcescope

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Plane is one host-initiated reader's policy.
type Plane struct {
	Budgets sandbox.SurveyBudgets
	// DeferredDirectories are traversed after ordinary directories.
	DeferredDirectories []string
	// CollapsedDirectories are left closed by a recursive expansion. Neither
	// set affects admission.
	CollapsedDirectories []string
	// IgnoreFiles excludes paths matched by project ignore rules.
	IgnoreFiles bool
	// DeferIgnored orders ignored directories after ordinary directories and
	// collapses them under a recursive expansion.
	DeferIgnored bool
}

// Config is the bundled source scope with the device overlay applied.
type Config struct {
	Capture Plane
	Catalog Plane
}

type budgetsYAML struct {
	DirectoryEntries *int `yaml:"directory_entries"`
	SubtreeEntries   *int `yaml:"subtree_entries"`
	WalkEntries      *int `yaml:"walk_entries"`
}

func (b budgetsYAML) apply(onto sandbox.SurveyBudgets) sandbox.SurveyBudgets {
	if b.DirectoryEntries != nil {
		onto.DirectoryEntries = *b.DirectoryEntries
	}
	if b.SubtreeEntries != nil {
		onto.SubtreeEntries = *b.SubtreeEntries
	}
	if b.WalkEntries != nil {
		onto.WalkEntries = *b.WalkEntries
	}
	return onto
}

type configYAML struct {
	Version int `yaml:"version"`
	Budgets struct {
		Capture budgetsYAML `yaml:"capture"`
		Catalog budgetsYAML `yaml:"catalog"`
	} `yaml:"budgets"`
	IgnoreFiles struct {
		Capture *bool `yaml:"capture"`
		// CatalogPriority reads ignore files to order the catalog walk and
		// collapse ignored trees.
		CatalogPriority *bool `yaml:"catalog_priority"`
	} `yaml:"ignore_files"`
}

func (y configYAML) apply(onto Config) Config {
	onto.Capture.Budgets = y.Budgets.Capture.apply(onto.Capture.Budgets)
	onto.Catalog.Budgets = y.Budgets.Catalog.apply(onto.Catalog.Budgets)
	if y.IgnoreFiles.Capture != nil {
		onto.Capture.IgnoreFiles = *y.IgnoreFiles.Capture
	}
	if y.IgnoreFiles.CatalogPriority != nil {
		onto.Catalog.DeferIgnored = *y.IgnoreFiles.CatalogPriority
	}
	// Catalog ignore rules order and collapse; they never exclude.
	onto.Catalog.IgnoreFiles = false
	return onto
}

// DefaultConfig returns the bundled source scope.
func DefaultConfig() (Config, error) {
	data, err := config.Read(config.SourceScope)
	if err != nil {
		return Config{}, fmt.Errorf("read source scope: %w", err)
	}
	var y configYAML
	if err := config.DecodeYAML(data, &y); err != nil {
		return Config{}, fmt.Errorf("parse source scope: %w", err)
	}
	cfg := y.apply(Config{})
	priority, err := loadDirectoryPriority()
	if err != nil {
		return Config{}, err
	}
	cfg.Catalog.DeferredDirectories, cfg.Catalog.CollapsedDirectories = priority.Deferred, priority.Collapsed
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", config.SourceScope, err)
	}
	return cfg, nil
}

// LoadConfig returns the bundled scope with the device overlay at path applied.
// A missing overlay is the bundled scope.
func LoadConfig(overlayPath string) (Config, error) {
	cfg, err := DefaultConfig()
	if err != nil {
		return Config{}, err
	}
	overlayPath = strings.TrimSpace(overlayPath)
	if overlayPath == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(overlayPath) // #nosec G304 -- device overlay under the engine config dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read source scope overlay: %w", err)
	}
	var y configYAML
	if err := config.DecodeYAML(data, &y); err != nil {
		return Config{}, fmt.Errorf("parse source scope overlay %s: %w", overlayPath, err)
	}
	cfg = y.apply(cfg)
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", overlayPath, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	if err := validateBudgets("capture", c.Capture.Budgets); err != nil {
		return err
	}
	return validateBudgets("catalog", c.Catalog.Budgets)
}

func validateBudgets(plane string, b sandbox.SurveyBudgets) error {
	if b.DirectoryEntries < 0 || b.SubtreeEntries < 0 || b.WalkEntries < 0 {
		return fmt.Errorf("budgets.%s: directory_entries, subtree_entries, and walk_entries must be nonnegative (zero is unbounded)", plane)
	}
	if b.SubtreeEntries > 0 && b.DirectoryEntries > b.SubtreeEntries {
		return fmt.Errorf("budgets.%s: directory_entries must not exceed subtree_entries", plane)
	}
	if b.WalkEntries > 0 && b.SubtreeEntries > b.WalkEntries {
		return fmt.Errorf("budgets.%s: subtree_entries must not exceed walk_entries", plane)
	}
	return nil
}
