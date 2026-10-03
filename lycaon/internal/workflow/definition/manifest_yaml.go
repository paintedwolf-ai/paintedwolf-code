package definition

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// LoadManifestFromFile reads one unresolved workflow manifest.
func LoadManifestFromFile(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifestYAML(data)
}

// ParseManifestYAML parses workflow manifest bytes.
func ParseManifestYAML(data []byte) (Manifest, error) {
	var wf workflowFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wf); err != nil {
		return Manifest{}, err
	}
	if err := validateWorkflowFileHeader(wf); err != nil {
		return Manifest{}, err
	}
	allowedAgents, agentToolAccess, err := parseAgentBindings(wf.ID, wf.Agents)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		ID:                 strings.TrimSpace(wf.ID),
		Version:            strings.TrimSpace(wf.Version),
		Retired:            wf.Retired,
		Extends:            strings.TrimSpace(wf.Extends),
		Name:               strings.TrimSpace(wf.Name),
		Description:        strings.TrimSpace(wf.Description),
		Trigger:            strings.TrimSpace(wf.Trigger),
		Icon:               strings.TrimSpace(wf.Icon),
		Featured:           wf.Featured,
		RequiresRepo:       wf.RequiresRepo,
		InitialPosture:     strings.TrimSpace(wf.InitialPosture),
		CoordinatorProfile: strings.TrimSpace(wf.CoordinatorProfile),
		SurfaceProfile:     strings.TrimSpace(wf.SurfaceProfile),
		AllowedAgents:      allowedAgents,
		AgentToolAccess:    agentToolAccess,
		Gates:              append([]string(nil), wf.Gates...),
		Rules:              append([]string(nil), wf.Rules...),
		Topology:           strings.TrimSpace(wf.Topology),
	}
	if err := assignManifestAttach(&m, wf.Attach); err != nil {
		return Manifest{}, err
	}
	m.Request, err = parseManifestRequest(wf.ID, wf.Request)
	if err != nil {
		return Manifest{}, err
	}
	controls, err := parseWorkflowControls(wf.ID, wf.Controls)
	if err != nil {
		return Manifest{}, err
	}
	if !controls.IsZero() {
		m.Controls = controls
	}
	m.Parameters, err = parseWorkflowParameters(wf.ID, wf.Parameters)
	if err != nil {
		return Manifest{}, err
	}
	m.Blueprint, err = parseManifestBlueprint(wf.ID, wf.Blueprint)
	if err != nil {
		return Manifest{}, err
	}
	if len(wf.Presets) > 0 {
		for _, raw := range wf.Presets {
			preset, err := parseManifestPresetYAML(wf.ID, raw)
			if err != nil {
				return Manifest{}, fmt.Errorf("workflow manifest %s: %w", wf.ID, err)
			}
			m.Presets = append(m.Presets, preset)
		}
	}
	for _, p := range wf.Phases {
		def, err := parsePhaseYAML(p)
		if err != nil {
			return Manifest{}, fmt.Errorf("workflow manifest %s: %w", wf.ID, err)
		}
		if def.ID == "" {
			continue
		}
		m.PhaseDefs = append(m.PhaseDefs, def)
	}
	if len(m.PhaseDefs) > 0 {
		m.Phases = rebuildPhaseOrder(m.PhaseDefs)
	}
	if err := assignWorkflowInjects(&m, wf.Injects); err != nil {
		return Manifest{}, err
	}
	if err := validateManifestSchema(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func parseAgentBindings(workflowID string, bindings []agentYAML) ([]string, map[string]sandbox.ToolAccess, error) {
	if len(bindings) == 0 {
		return nil, nil, nil
	}
	allowed := make([]string, 0, len(bindings))
	access := make(map[string]sandbox.ToolAccess, len(bindings))
	for _, binding := range bindings {
		id := strings.TrimSpace(binding.ID)
		if id == "" {
			return nil, nil, fmt.Errorf("workflow manifest %s: agent id required", workflowID)
		}
		if _, exists := access[id]; exists {
			return nil, nil, fmt.Errorf("workflow manifest %s: duplicate agent %q", workflowID, id)
		}
		mode := binding.Tools
		if mode == "" {
			return nil, nil, fmt.Errorf("workflow manifest %s: agent %q tools required (all or profile)", workflowID, id)
		}
		if mode != sandbox.ToolAccessAll && mode != sandbox.ToolAccessProfile {
			return nil, nil, fmt.Errorf("workflow manifest %s: agent %q tools must be all or profile", workflowID, id)
		}
		if binding.Spawn == nil || *binding.Spawn {
			allowed = append(allowed, id)
		}
		access[id] = mode
	}
	return allowed, access, nil
}

// parseWorkflowControls builds workflow-level runtime controls.
func parseWorkflowControls(workflowID string, raw workflowControls) (ManifestControls, error) {
	controls := ManifestControls{}
	if pa := strings.TrimSpace(raw.PhaseAdvance); pa != "" {
		switch PhaseAdvancePolicy(pa) {
		case PhaseAdvanceHost:
			controls.PhaseAdvance = PhaseAdvanceHost
		default:
			return ManifestControls{}, fmt.Errorf("workflow manifest %s: invalid controls.phase_advance %q (want host)", workflowID, pa)
		}
	}
	if raw.OnDecisionReject != nil {
		controls.OnDecisionReject = &DecisionRejectControls{
			Pause:  raw.OnDecisionReject.Pause,
			Cancel: raw.OnDecisionReject.Cancel,
		}
	}
	if raw.OnPause != nil {
		controls.OnPause = &PauseControls{
			HoldPending:   raw.OnPause.HoldPending,
			CancelRunning: raw.OnPause.CancelRunning,
		}
	}
	if raw.OnStop != nil {
		controls.OnStop = &StopControls{
			CancelWorkers:   raw.OnStop.CancelWorkers,
			AbortDelegation: raw.OnStop.AbortDelegation,
			SessionAbort:    raw.OnStop.SessionAbort,
		}
	}
	if raw.ContentReview != nil {
		controls.ContentReview = &PhaseContentReview{
			Tools: append([]string(nil), raw.ContentReview.Tools...),
			Paths: append([]string(nil), raw.ContentReview.Paths...),
		}
	}
	if dem := strings.TrimSpace(raw.DefaultExecutionMode); dem != "" {
		if err := ValidateExecutionModeField("controls.default_execution_mode", dem); err != nil {
			return ManifestControls{}, fmt.Errorf("workflow manifest %s: %w", workflowID, err)
		}
		controls.DefaultExecutionMode = NormalizeExecutionMode(dem)
	}
	if raw.Report != nil {
		brief, err := parseBriefYAML(raw.Report.Brief)
		if err != nil {
			return ManifestControls{}, fmt.Errorf("workflow manifest %s: %w", workflowID, err)
		}
		controls.Report = &ReportControls{
			Enabled:       raw.Report.Enabled,
			FindingsLabel: strings.TrimSpace(raw.Report.FindingsLabel),
			Brief:         brief,
		}
	}
	return controls, nil
}

// parseWorkflowParameters resolves declared parameters.
func parseWorkflowParameters(workflowID string, raw map[string]workflowParameterYAML) (map[string]WorkflowParameter, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	params := make(map[string]WorkflowParameter, len(raw))
	for name, spec := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("workflow manifest %s: empty parameters key", workflowID)
		}
		params[name] = WorkflowParameter{
			Type:    strings.TrimSpace(spec.Type),
			Default: strings.TrimSpace(spec.Default),
		}
	}
	return params, nil
}

// parseManifestBlueprint resolves the governing document a workflow's gates read.
func parseManifestBlueprint(workflowID string, raw *blueprintYAML) (*BlueprintDef, error) {
	if raw == nil {
		return nil, nil
	}
	bp := &BlueprintDef{
		ID:          strings.TrimSpace(raw.ID),
		Frontmatter: append([]string(nil), raw.Frontmatter...),
	}
	if file := strings.TrimSpace(raw.File); file != "" {
		bp.Path = blueprint.ConventionPath(file)
	}
	if bp.ID == "" {
		return nil, fmt.Errorf("workflow manifest %s: blueprint id required", workflowID)
	}
	if bp.Path != "" {
		if err := blueprint.ValidateConventionPath(bp.Path); err != nil {
			return nil, fmt.Errorf("workflow manifest %s: blueprint.file: %w", workflowID, err)
		}
	}
	return bp, nil
}

var manifestIdentifier = regexp.MustCompile(`^[a-z0-9_-]+$`)

func validateWorkflowFileHeader(wf workflowFile) error {
	if strings.TrimSpace(wf.ID) == "" {
		return fmt.Errorf("workflow manifest: id required")
	}
	if !manifestIdentifier.MatchString(strings.TrimSpace(wf.ID)) {
		return fmt.Errorf("workflow manifest: id %q must contain only lowercase letters, digits, underscores, or hyphens", wf.ID)
	}
	if strings.TrimSpace(wf.Version) == "" {
		return fmt.Errorf("workflow manifest %s: version required", wf.ID)
	}
	if _, err := semver.StrictNewVersion(strings.TrimSpace(wf.Version)); err != nil {
		return fmt.Errorf("workflow manifest %s: version must be canonical semantic version: %w", wf.ID, err)
	}
	return nil
}

func assignManifestAttach(m *Manifest, raw *attachYAML) error {
	if raw == nil {
		return nil
	}
	policy := parseAttachPolicy(raw.Policy)
	if !policy.valid() {
		return fmt.Errorf("workflow manifest %s: invalid attach.policy %q", m.ID, raw.Policy)
	}
	m.Attach = ManifestAttach{Policy: policy}
	return nil
}

func assignWorkflowInjects(m *Manifest, raw []anchor.WorkflowInject) error {
	if m == nil || len(raw) == 0 {
		return nil
	}
	m.Injects = append([]anchor.WorkflowInject(nil), raw...)
	for i := range m.Injects {
		if _, err := m.Injects[i].Binding(m.ID); err != nil {
			return fmt.Errorf("workflow manifest %s: injects[%d]: %w", m.ID, i, err)
		}
	}
	return nil
}

func parseManifestPresetYAML(manifestID string, raw manifestPresetYAML) (ManifestPreset, error) {
	id := strings.TrimSpace(raw.ID)
	if id == "" {
		return ManifestPreset{}, fmt.Errorf("preset id required")
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = id
	}
	params := map[string]string{}
	for k, v := range raw.Params {
		k = strings.TrimSpace(k)
		if k != "" {
			params[k] = strings.TrimSpace(v)
		}
	}
	if len(params) == 0 {
		return ManifestPreset{}, fmt.Errorf("preset %q on %s: params required", id, manifestID)
	}
	return ManifestPreset{
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(raw.Description),
		Trigger:     strings.TrimSpace(raw.Trigger),
		Params:      params,
	}, nil
}

func uniqueAgentIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
