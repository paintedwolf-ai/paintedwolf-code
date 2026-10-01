package definition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ManifestPreset is a named parameter bundle on a parent workflow manifest.
type ManifestPreset struct {
	ID          string
	Name        string
	Description string
	Trigger     string
	Params      map[string]string
}

// TriggerMatch resolves a slash trigger to a manifest and optional preset.
type TriggerMatch struct {
	Manifest Manifest
	PresetID string
}

// FindTriggerMatch returns the manifest (and preset when applicable) for an exact trigger.
func (r *Registry) FindTriggerMatch(trigger string) (TriggerMatch, bool) {
	if r == nil {
		return TriggerMatch{}, false
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		return TriggerMatch{}, false
	}
	type cand struct {
		manifest Manifest
		presetID string
		priority int
	}
	var matches []cand
	for _, m := range r.current() {
		if strings.TrimSpace(m.Trigger) == trigger {
			matches = append(matches, cand{manifest: m, priority: 1})
		}
		for _, p := range m.Presets {
			if strings.TrimSpace(p.Trigger) == trigger {
				matches = append(matches, cand{manifest: m, presetID: p.ID, priority: 2 + len(p.ID)})
			}
		}
	}
	if len(matches) == 0 {
		return TriggerMatch{}, false
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].priority != matches[j].priority {
			return matches[i].priority > matches[j].priority
		}
		if versionOrder := compareManifestVersions(matches[i].manifest.Version, matches[j].manifest.Version); versionOrder != 0 {
			return versionOrder > 0
		}
		if matches[i].manifest.ID != matches[j].manifest.ID {
			return matches[i].manifest.ID < matches[j].manifest.ID
		}
		return matches[i].presetID < matches[j].presetID
	})
	best := matches[0]
	return TriggerMatch{Manifest: cloneManifest(best.manifest), PresetID: best.presetID}, true
}

// PresetByID returns a preset declared on manifest id@version.
func (r *Registry) PresetByID(id, version, presetID string) (ManifestPreset, Manifest, bool) {
	if r == nil {
		return ManifestPreset{}, Manifest{}, false
	}
	m, err := r.Get(id, version)
	if err != nil {
		return ManifestPreset{}, Manifest{}, false
	}
	presetID = strings.TrimSpace(presetID)
	for _, p := range m.Presets {
		if p.ID == presetID {
			return p, m, true
		}
	}
	return ManifestPreset{}, Manifest{}, false
}

// MergeStartParams resolves validated start parameters.
func MergeStartParams(manifest Manifest, presetID string, req api.StartWorkflowRunRequest) (map[string]string, error) {
	out := map[string]string{}
	for name, spec := range manifest.Parameters {
		val := strings.TrimSpace(spec.Default)
		if spec.Type == "depth" && val == "" {
			val = string(DepthLight)
		}
		if val != "" {
			normalized, err := normalizeWorkflowParameter(spec, val)
			if err != nil {
				return nil, fmt.Errorf("%w: parameter %q default: %w", ErrWorkflowParameterInvalid, name, err)
			}
			out[name] = normalized
		}
	}
	if presetID != "" {
		found := false
		for _, p := range manifest.Presets {
			if p.ID == presetID {
				found = true
				for k, v := range p.Params {
					k = strings.TrimSpace(k)
					spec, ok := manifest.Parameters[k]
					if !ok {
						return nil, fmt.Errorf("%w: preset %q names unknown parameter %q", ErrWorkflowParameterInvalid, presetID, k)
					}
					normalized, err := normalizeWorkflowParameter(spec, v)
					if err != nil {
						return nil, fmt.Errorf("%w: preset %q parameter %q: %w", ErrWorkflowParameterInvalid, presetID, k, err)
					}
					out[k] = normalized
				}
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: unknown preset %q", ErrWorkflowParameterInvalid, presetID)
		}
	}
	for k, v := range req.Parameters {
		k = strings.TrimSpace(k)
		spec, ok := manifest.Parameters[k]
		if !ok {
			return nil, fmt.Errorf("%w: unknown parameter %q", ErrWorkflowParameterInvalid, k)
		}
		normalized, err := normalizeWorkflowParameter(spec, v)
		if err != nil {
			return nil, fmt.Errorf("%w: parameter %q: %w", ErrWorkflowParameterInvalid, k, err)
		}
		out[k] = normalized
	}
	return out, nil
}

func normalizeWorkflowParameter(spec WorkflowParameter, raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", fmt.Errorf("value is required")
	}
	switch strings.TrimSpace(spec.Type) {
	case "depth":
		level, err := ParseDepthLevel(raw)
		return string(level), err
	case "boolean":
		if raw != "true" && raw != "false" {
			return "", fmt.Errorf("invalid boolean %q (want true|false)", raw)
		}
		return raw, nil
	default:
		return "", fmt.Errorf("unsupported parameter type %q", spec.Type)
	}
}
