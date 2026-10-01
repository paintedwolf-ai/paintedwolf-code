// Package clientnotice loads bundled notice copy.
package clientnotice

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Scope identifies notice placement.
type Scope string

const (
	ScopeApp     Scope = "app"
	ScopeProject Scope = "project"
	ScopeSession Scope = "session"
)

// Scenario defines required phrases for a copy variant.
type Scenario struct {
	ID             string   `yaml:"id"`
	ExpectContains []string `yaml:"expect_contains"`
}

// Notice defines one generated client notice.
type Notice struct {
	Kind            string `yaml:"-"`
	Scope           Scope  `yaml:"scope"`
	Title           string `yaml:"title"`
	Message         string `yaml:"message"`
	SuggestedAction string `yaml:"suggested_action"`
	// Action identifies an in-app destination.
	Action    string     `yaml:"action"`
	Scenarios []Scenario `yaml:"scenarios"`
}

// Catalog is every client notice, ordered by kind so codegen output is stable.
type Catalog struct {
	Notices []Notice
}

// Load reads every YAML in the bundled client-notices directory.
func Load() (*Catalog, error) {
	entries, err := config.List(config.ClientNoticesDir)
	if err != nil {
		return nil, fmt.Errorf("client notices dir: %w", err)
	}

	var notices []Notice
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		raw, err := config.Read(config.ClientNoticesDir.Join(name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var n Notice
		if err := config.DecodeYAML(raw, &n); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		n.Kind = strings.TrimSuffix(name, ".yaml")
		if err := n.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		notices = append(notices, n)
	}
	if len(notices) == 0 {
		return nil, fmt.Errorf("no client notices found in %s", config.ClientNoticesDir)
	}
	sort.Slice(notices, func(i, j int) bool { return notices[i].Kind < notices[j].Kind })
	return &Catalog{Notices: notices}, nil
}

func (n Notice) validate() error {
	if strings.TrimSpace(n.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(n.Message) == "" {
		return fmt.Errorf("message is required")
	}
	switch n.Scope {
	case ScopeApp, ScopeProject, ScopeSession:
	default:
		return fmt.Errorf("scope must be app, project, or session (got %q)", n.Scope)
	}
	if n.Action != "" && n.Action != "retry_contribution_frame" {
		return fmt.Errorf("action must be retry_contribution_frame (got %q)", n.Action)
	}
	for _, sc := range n.Scenarios {
		if len(sc.ExpectContains) == 0 {
			return fmt.Errorf("scenario %q has no expect_contains", sc.ID)
		}
	}
	return nil
}

// Rendered returns searchable user-facing copy.
func (n Notice) Rendered() string {
	return strings.TrimSpace(n.Title + "\n" + n.Message + "\n" + n.SuggestedAction)
}
