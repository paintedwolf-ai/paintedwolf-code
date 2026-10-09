package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// PostureSpec is one entry from session-postures.yaml. A posture names a stage
// and its invoke rules; the tool surface comes from the workflow manifest or the
// session's agent.
type PostureSpec struct {
	ID          api.SessionPosture `yaml:"-"`
	Description string             `yaml:"description"`
	Rules       []string           `yaml:"rules"`
	Label       string             `yaml:"label"`
}

type posturesFile struct {
	Postures map[string]postureEntryYAML `yaml:"postures"`
}

type postureEntryYAML struct {
	Description string   `yaml:"description"`
	Rules       []string `yaml:"rules"`
	Label       string   `yaml:"label"`
}

// PostureRegistry resolves posture id → default rules.
type PostureRegistry struct {
	entries map[api.SessionPosture]PostureSpec
}

// LoadPostureRegistry reads the shipped session postures.
func LoadPostureRegistry() (*PostureRegistry, error) {
	data, err := config.Read(config.SessionPostures)
	if err != nil {
		return nil, err
	}
	var raw posturesFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, err
	}
	if len(raw.Postures) == 0 {
		return nil, fmt.Errorf("session-postures: postures required")
	}
	entries := make(map[api.SessionPosture]PostureSpec, len(raw.Postures))
	for id, entry := range raw.Postures {
		id = strings.TrimSpace(id)
		if !sessionposture.ValidSessionPosture(id) {
			return nil, fmt.Errorf("session-postures: unknown posture %q", id)
		}
		posture := api.SessionPosture(id)
		entries[posture] = PostureSpec{
			ID:          posture,
			Description: strings.TrimSpace(entry.Description),
			Rules:       append([]string(nil), entry.Rules...),
			Label:       strings.TrimSpace(entry.Label),
		}
	}
	for _, p := range sessionposture.AllSessionPostures() {
		if _, ok := entries[p]; !ok {
			return nil, fmt.Errorf("session-postures: missing posture %q", p)
		}
	}
	return &PostureRegistry{entries: entries}, nil
}

func mergePostureOverlay(base *PostureRegistry, projectDir string) (*PostureRegistry, error) {
	if base == nil || strings.TrimSpace(projectDir) == "" {
		return base, nil
	}
	path := filepath.Join(projectDir, settingsoverlay.DirName(), "postures.yaml")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return base, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw posturesFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, err
	}
	out := &PostureRegistry{entries: map[api.SessionPosture]PostureSpec{}}
	for k, v := range base.entries {
		out.entries[k] = v
	}
	for id, entry := range raw.Postures {
		id = strings.TrimSpace(id)
		if !sessionposture.ValidSessionPosture(id) {
			return nil, fmt.Errorf("project postures: unknown posture %q", id)
		}
		posture := api.SessionPosture(id)
		cur := out.entries[posture]
		if len(entry.Rules) > 0 {
			cur.Rules = append([]string(nil), entry.Rules...)
		}
		if d := strings.TrimSpace(entry.Description); d != "" {
			cur.Description = d
		}
		if l := strings.TrimSpace(entry.Label); l != "" {
			cur.Label = l
		}
		cur.ID = posture
		out.entries[posture] = cur
	}
	return out, nil
}

// MergePostureOverlays applies posture files in root order.
func MergePostureOverlays(base *PostureRegistry, projectDirs []string) (*PostureRegistry, error) {
	merged := base
	seen := map[string]struct{}{}
	for _, projectDir := range projectDirs {
		projectDir = strings.TrimSpace(projectDir)
		if projectDir == "" {
			continue
		}
		if _, exists := seen[projectDir]; exists {
			continue
		}
		seen[projectDir] = struct{}{}
		var err error
		merged, err = mergePostureOverlay(merged, projectDir)
		if err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// RulesPaths returns bundled rule file paths for a posture.
func (r *PostureRegistry) RulesPaths(posture api.SessionPosture) ([]string, error) {
	spec, err := r.Get(posture)
	if err != nil {
		return nil, err
	}
	if len(spec.Rules) == 0 {
		return nil, fmt.Errorf("posture %q: rules required", posture)
	}
	return append([]string(nil), spec.Rules...), nil
}

// Get returns a posture spec.
func (r *PostureRegistry) Get(posture api.SessionPosture) (PostureSpec, error) {
	if r == nil {
		return PostureSpec{}, fmt.Errorf("posture registry not loaded")
	}
	spec, ok := r.entries[posture]
	if !ok {
		return PostureSpec{}, fmt.Errorf("unknown posture %q", posture)
	}
	return spec, nil
}

// List returns all posture specs sorted by id.
func (r *PostureRegistry) List() []PostureSpec {
	if r == nil {
		return nil
	}
	out := make([]PostureSpec, 0, len(r.entries))
	for _, s := range r.entries {
		out = append(out, s)
	}
	return out
}

// AllSessionPostures is the closed posture enum.

// ValidSessionPosture reports whether s is a known SessionPosture value.
