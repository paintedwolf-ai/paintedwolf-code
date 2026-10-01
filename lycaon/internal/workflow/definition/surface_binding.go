package definition

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

// ResolvedSurfaceBinding is the manifest-driven coordinator surface binding for one phase.
type ResolvedSurfaceBinding struct {
	CoordinatorSurface          string
	SurfaceTemplate             string
	ModeRefs                    []string
	WorkflowInvestigateEligible bool
	ProfileResolved             bool
}

// ResolveSurfaceBinding resolves per-phase surface binding from manifest fields and profile defaults.
func ResolveSurfaceBinding(m Manifest, phaseID, workflowID, configRoot string) (ResolvedSurfaceBinding, error) {
	profiles, err := surface.LoadSurfaceProfiles(configRoot)
	if err != nil {
		return ResolvedSurfaceBinding{}, err
	}
	profileName := strings.TrimSpace(m.SurfaceProfile)
	if profileName == "" && strings.TrimSpace(workflowID) == "" {
		profileName = "implement"
	}
	var profile surface.SurfaceProfile
	var hasProfile bool
	if profileName != "" {
		profile, hasProfile = profiles[profileName]
		if !hasProfile {
			return ResolvedSurfaceBinding{}, fmt.Errorf("workflow manifest %s: unknown surface_profile %q", m.ID, profileName)
		}
	}
	phaseID = strings.TrimSpace(phaseID)
	phase, hasPhase := m.PhaseByID(phaseID)
	var phaseBinding surface.SurfaceProfilePhaseBinding
	if hasProfile {
		phaseBinding = profile.Phases[phaseID]
	}
	out := ResolvedSurfaceBinding{
		ProfileResolved: hasProfile,
	}
	if hasProfile {
		out.WorkflowInvestigateEligible = profile.InvestigateEligible
	}
	if hasPhase {
		if s := strings.TrimSpace(phase.CoordinatorSurface); s != "" {
			out.CoordinatorSurface = s
		} else if phaseBinding.Surface != "" {
			out.CoordinatorSurface = phaseBinding.Surface
		}
		if t := strings.TrimSpace(phase.SurfaceTemplate); t != "" {
			out.SurfaceTemplate = t
		} else if phaseBinding.Template != "" {
			out.SurfaceTemplate = phaseBinding.Template
		}
		if len(phase.ModeRefs) > 0 {
			out.ModeRefs = append([]string(nil), phase.ModeRefs...)
		} else if len(phaseBinding.ModeRefs) > 0 {
			out.ModeRefs = append([]string(nil), phaseBinding.ModeRefs...)
		}
	}
	return out, nil
}

func validateManifestSurfaceBinding(m Manifest, knownSurfaces map[string]struct{}, knownProfiles map[string]surface.SurfaceProfile) error {
	if sp := strings.TrimSpace(m.SurfaceProfile); sp != "" {
		if _, ok := knownProfiles[sp]; !ok {
			return fmt.Errorf("workflow manifest %s: unknown surface_profile %q", m.ID, sp)
		}
	}
	for _, p := range m.PhaseDefs {
		if err := validatePhaseSurfaceBinding(m.ID, p, knownSurfaces); err != nil {
			return err
		}
	}
	return nil
}

func validatePhaseSurfaceBinding(manifestID string, p PhaseDef, knownSurfaces map[string]struct{}) error {
	if s := strings.TrimSpace(p.CoordinatorSurface); s != "" {
		if _, ok := knownSurfaces[s]; !ok {
			return fmt.Errorf("workflow manifest %s: phase %q: unknown coordinator_surface %q", manifestID, p.ID, s)
		}
	}
	if t := strings.TrimSpace(p.SurfaceTemplate); t == "" && strings.TrimSpace(p.CoordinatorSurface) != "" {
		return fmt.Errorf("workflow manifest %s: phase %q: coordinator_surface requires surface_template", manifestID, p.ID)
	}
	for _, ref := range p.ModeRefs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("workflow manifest %s: phase %q: empty mode_refs entry", manifestID, p.ID)
		}
	}
	return nil
}
