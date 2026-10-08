// Package definition parses, resolves, validates, and snapshots workflow definitions and their catalog.
package definition

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// Manifest describes a workflow definition loaded from YAML.
type Manifest struct {
	ID      string
	Version string
	// Format is the manifest document schema version (defaults to 1).
	Format int
	// Retired definitions remain available to existing runs but leave the start catalog.
	Retired bool
	// Sealed marks a superseded release kept as an immutable archive copy.
	Sealed bool
	// ArchiveDir records the directory of the sealed copy if Sealed is true.
	ArchiveDir         string
	Attach             ManifestAttach
	Request            *ManifestRequest
	Extends            string
	Name               string
	Description        string
	Trigger            string
	Icon               string
	Featured           *bool
	RequiresRepo       *bool
	InitialPosture     string
	CoordinatorProfile string
	SurfaceProfile     string
	Phases             []string
	PhaseDefs          []PhaseDef
	AllowedAgents      []string
	AgentToolAccess    map[string]sandbox.ToolAccess
	Rules              []string
	Gates              []string
	Controls           ManifestControls
	Topology           string
	Parameters         map[string]WorkflowParameter
	Blueprint          *BlueprintDef
	Presets            []ManifestPreset
	Injects            []anchor.WorkflowInject
}

func ManifestKey(id, version string) string {
	return strings.ToLower(strings.TrimSpace(id)) + "@" + strings.TrimSpace(version)
}

// Registry resolves workflow manifests from the active catalog.
type Registry struct {
	mu       sync.RWMutex
	entries  map[string]Manifest
	revision string
	// derive rebuilds entries from the active catalog.
	derive func() (map[string]Manifest, error)
}

// NewRegistry creates a fixed manifest registry over explicit entries.
func NewRegistry(entries map[string]Manifest) *Registry {
	return &Registry{entries: cloneManifests(entries)}
}

func cloneManifests(entries map[string]Manifest) map[string]Manifest {
	cloned := make(map[string]Manifest, len(entries))
	for key, manifest := range entries {
		mustManifestVersion(manifest.Version)
		expected := ManifestKey(manifest.ID, manifest.Version)
		if key != expected {
			panic(fmt.Sprintf("workflow manifest key %q does not match %q", key, expected))
		}
		cloned[key] = cloneManifest(manifest)
	}
	return cloned
}

// current returns entries for the active catalog generation.
func (r *Registry) current() map[string]Manifest {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	entries, revision, derive := r.entries, r.revision, r.derive
	r.mu.RUnlock()
	if derive == nil {
		return entries
	}
	live := activeCatalogRevision()
	if live == revision {
		return entries
	}
	next, err := derive()
	if err != nil {
		// Failed resolution preserves the active entries.
		return entries
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries, r.revision = cloneManifests(next), live
	return r.entries
}

// activeCatalogRevision identifies the catalog generation in use.
func activeCatalogRevision() string {
	if eff := extpacks.Active(); eff != nil {
		return eff.Revision
	}
	return ""
}

// Get returns a manifest or ErrUnknownWorkflow.
func (r *Registry) Get(id, version string) (Manifest, error) {
	if r == nil {
		return Manifest{}, ErrUnknownWorkflow
	}
	m, ok := r.current()[ManifestKey(id, version)]
	if !ok {
		return Manifest{}, fmt.Errorf("%w: %s@%s", ErrUnknownWorkflow, id, version)
	}
	return cloneManifest(m), nil
}

// CatalogStartable reports whether the user may start id@version directly:
// any catalog-visible workflow, bundled or project overlay, is startable.
func (r *Registry) CatalogStartable(id, version string) bool {
	if r == nil {
		return false
	}
	version = strings.TrimSpace(version)
	m, err := r.Get(id, version)
	if err != nil {
		return false
	}
	return m.IsCatalogVisible()
}

// All returns manifests keyed by id@version.
func (r *Registry) All() map[string]Manifest {
	if r == nil {
		return nil
	}
	entries := r.current()
	out := make(map[string]Manifest, len(entries))
	for key, manifest := range entries {
		out[key] = cloneManifest(manifest)
	}
	return out
}

// List returns all manifests sorted by id ascending, then version descending.
func (r *Registry) List() []Manifest {
	entries := r.current()
	if len(entries) == 0 {
		return nil
	}
	out := make([]Manifest, 0, len(entries))
	for _, m := range entries {
		out = append(out, cloneManifest(m))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return compareManifestVersions(out[i].Version, out[j].Version) > 0
	})
	return out
}

// SummariesWithScopes returns catalog DTOs annotated with manifest tier scope.
func (r *Registry) SummariesWithScopes(scopes map[string]string) []api.WorkflowSummary {
	list := r.List()
	out := make([]api.WorkflowSummary, 0, len(list))
	for _, m := range list {
		s := m.Summary()
		if scopes != nil {
			key := ManifestKey(m.ID, m.Version)
			if scope := scopes[key]; scope != "" {
				s.Scope = api.WorkflowScope(scope)
			} else {
				s.Scope = api.WorkflowScopeBundled
			}
		}
		out = append(out, s)
	}
	return out
}

// Summary maps a manifest to the public catalog DTO.
func (m Manifest) Summary() api.WorkflowSummary {
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = m.ID
	}
	return api.WorkflowSummary{
		ID:                 m.ID,
		Version:            m.Version,
		Name:               name,
		Description:        strings.TrimSpace(m.Description),
		Trigger:            strings.TrimSpace(m.Trigger),
		Request:            m.Request.Summary(),
		InitialPosture:     strings.TrimSpace(m.InitialPosture),
		Topology:           strings.TrimSpace(m.Topology),
		Icon:               strings.TrimSpace(m.Icon),
		Featured:           manifestBool(m.Featured),
		RequiresRepo:       manifestBool(m.RequiresRepo),
		SupportsBlueprints: SupportsBlueprints(m),
		ReportEnabled:      m.ReportEnabled(),
		Phases:             append([]string(nil), m.Phases...),
		Presets:            presetSummaries(m.Presets),
	}
}

func manifestBool(value *bool) bool {
	return value != nil && *value
}

// ReportEnabled reports whether the manifest enables reports.
func (m Manifest) ReportEnabled() bool {
	return m.Controls.Report != nil && m.Controls.Report.Enabled
}

// ReportFindingsLabel is what this workflow calls its assessed conclusions.
// Empty when the manifest declared none, and the renderer names them itself.
func (m Manifest) ReportFindingsLabel() string {
	if m.Controls.Report == nil {
		return ""
	}
	return m.Controls.Report.FindingsLabel
}

// ReportBrief is the rating the manifest declares for its report, or nil.
func (m Manifest) ReportBrief() *Brief {
	if m.Controls.Report == nil {
		return nil
	}
	return m.Controls.Report.Brief
}

func compareManifestVersions(left, right string) int {
	return mustManifestVersion(left).Compare(mustManifestVersion(right))
}

func mustManifestVersion(raw string) *semver.Version {
	version, err := semver.StrictNewVersion(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("invalid workflow manifest version %q: %v", raw, err))
	}
	return version
}

func presetSummaries(presets []ManifestPreset) []api.WorkflowPresetSummary {
	if len(presets) == 0 {
		return nil
	}
	out := make([]api.WorkflowPresetSummary, 0, len(presets))
	for _, p := range presets {
		out = append(out, api.WorkflowPresetSummary{
			ID:          p.ID,
			Name:        p.Name,
			Description: strings.TrimSpace(p.Description),
			Trigger:     strings.TrimSpace(p.Trigger),
		})
	}
	return out
}

func (m Manifest) FirstPhase() string {
	if len(m.Phases) == 0 {
		return "stub"
	}
	return m.Phases[0]
}

func (m Manifest) NextPhase(current string) (string, bool) {
	current = strings.TrimSpace(current)
	if def, ok := m.PhaseByID(current); ok {
		if next := strings.TrimSpace(def.Next); next != "" {
			if _, ok := m.PhaseByID(next); ok {
				return next, true
			}
		}
	}
	for i, p := range m.Phases {
		if p == current && i+1 < len(m.Phases) {
			return m.Phases[i+1], true
		}
	}
	return "", false
}

// SupportsBlueprints reports whether the manifest declares a blueprint.
func SupportsBlueprints(m Manifest) bool {
	return m.Blueprint != nil
}
