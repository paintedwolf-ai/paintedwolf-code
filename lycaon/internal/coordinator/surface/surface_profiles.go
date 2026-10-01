package surface

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// SurfaceProfilePhaseBinding is the per-phase default within a surface profile.
type SurfaceProfilePhaseBinding struct {
	Surface  string
	Template string
	ModeRefs []string
}

// SurfaceProfile is a named coordinator surface family default.
type SurfaceProfile struct {
	InvestigateEligible bool
	Phases              map[string]SurfaceProfilePhaseBinding
}

type surfaceProfilesYAML struct {
	Profiles map[string]struct {
		InvestigateEligible bool `yaml:"investigate_eligible"`
		Phases              map[string]struct {
			Surface  string   `yaml:"surface"`
			Template string   `yaml:"template"`
			ModeRefs []string `yaml:"mode_refs"`
		} `yaml:"phases"`
	} `yaml:",inline"`
}

// surfaceProfilesDecoded memoizes the YAML decode against the bytes it decoded:
// manifest validation calls this once per manifest on every coordinator turn.
var surfaceProfilesDecoded struct {
	mu     sync.Mutex
	raw    []byte
	parsed *surfaceProfilesYAML
}

func decodeSurfaceProfiles(data []byte) (*surfaceProfilesYAML, error) {
	surfaceProfilesDecoded.mu.Lock()
	defer surfaceProfilesDecoded.mu.Unlock()
	if surfaceProfilesDecoded.parsed != nil && bytes.Equal(surfaceProfilesDecoded.raw, data) {
		return surfaceProfilesDecoded.parsed, nil
	}
	raw := &surfaceProfilesYAML{}
	if err := config.DecodeYAML(data, raw); err != nil {
		return nil, err
	}
	surfaceProfilesDecoded.raw = append([]byte(nil), data...)
	surfaceProfilesDecoded.parsed = raw
	return raw, nil
}

// LoadSurfaceProfiles decodes surface-profiles.yaml from the process config
// source. There is no Go default.
func LoadSurfaceProfiles(configRoot string) (map[string]SurfaceProfile, error) {
	data, err := config.Read(config.SurfaceProfiles)
	if err != nil {
		return nil, fmt.Errorf("read surface profiles: %w", err)
	}
	raw, err := decodeSurfaceProfiles(data)
	if err != nil {
		return nil, fmt.Errorf("parse surface profiles: %w", err)
	}
	// Rebuilt every call so callers never share the memoized decode's maps.
	out := make(map[string]SurfaceProfile, len(raw.Profiles))
	for name, row := range raw.Profiles {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		profile := SurfaceProfile{
			InvestigateEligible: row.InvestigateEligible,
			Phases:              map[string]SurfaceProfilePhaseBinding{},
		}
		for phaseID, binding := range row.Phases {
			phaseID = strings.TrimSpace(phaseID)
			if phaseID == "" {
				continue
			}
			refs := make([]string, 0, len(binding.ModeRefs))
			for _, ref := range binding.ModeRefs {
				ref = strings.TrimSpace(ref)
				if ref != "" {
					refs = append(refs, ref)
				}
			}
			profile.Phases[phaseID] = SurfaceProfilePhaseBinding{
				Surface:  strings.TrimSpace(binding.Surface),
				Template: strings.TrimSpace(binding.Template),
				ModeRefs: refs,
			}
		}
		out[name] = profile
	}
	return out, nil
}
