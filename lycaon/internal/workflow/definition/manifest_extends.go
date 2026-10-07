package definition

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// ResolveManifestChain merges extends ancestors into a single effective manifest.
func ResolveManifestChain(m Manifest, catalog map[string]Manifest) (Manifest, error) {
	visited := map[string]struct{}{}
	return resolveManifest(m, catalog, 0, visited)
}

func resolveManifest(m Manifest, catalog map[string]Manifest, depth int, visited map[string]struct{}) (Manifest, error) {
	ext := strings.TrimSpace(m.Extends)
	if ext == "" {
		return FinalizeManifest(m), nil
	}
	if depth >= maxExtendsDepth {
		return Manifest{}, &ExtendsError{
			Kind:            ExtendsErrorDepth,
			Ref:             ext,
			ManifestID:      m.ID,
			ManifestVersion: m.Version,
			MaxDepth:        maxExtendsDepth,
		}
	}
	if _, cycle := visited[ext]; cycle {
		return Manifest{}, &ExtendsError{Kind: ExtendsErrorCycle, Ref: ext, ManifestID: m.ID, ManifestVersion: m.Version}
	}
	parent, ok := catalog[ext]
	if !ok {
		return Manifest{}, &ExtendsError{Kind: ExtendsErrorUnknown, Ref: ext, ManifestID: m.ID, ManifestVersion: m.Version}
	}
	visited[ext] = struct{}{}
	parentResolved, err := resolveManifest(parent, catalog, depth+1, visited)
	delete(visited, ext)
	if err != nil {
		return Manifest{}, err
	}
	merged := mergeManifest(parentResolved, m)
	merged.Extends = ""
	return FinalizeManifest(merged), nil
}

func mergeManifest(parent, child Manifest) Manifest {
	out := cloneManifest(parent)
	out.Retired = child.Retired
	out.ID = child.ID
	out.Version = child.Version
	// The child controls attachments.
	out.Attach = child.Attach
	if child.Request != nil {
		out.Request = cloneManifestRequest(child.Request)
	}
	if strings.TrimSpace(child.Name) != "" {
		out.Name = child.Name
	}
	if strings.TrimSpace(child.Description) != "" {
		out.Description = child.Description
	}
	if strings.TrimSpace(child.Trigger) != "" {
		out.Trigger = child.Trigger
	}
	if strings.TrimSpace(child.InitialPosture) != "" {
		out.InitialPosture = child.InitialPosture
	}
	if strings.TrimSpace(child.Icon) != "" {
		out.Icon = child.Icon
	}
	if child.Featured != nil {
		out.Featured = cloneBoolPointer(child.Featured)
	}
	if child.RequiresRepo != nil {
		out.RequiresRepo = cloneBoolPointer(child.RequiresRepo)
	}
	if cp := strings.TrimSpace(child.CoordinatorProfile); cp != "" {
		out.CoordinatorProfile = cp
	}
	if sp := strings.TrimSpace(child.SurfaceProfile); sp != "" {
		out.SurfaceProfile = sp
	}
	if topo := strings.TrimSpace(child.Topology); topo != "" {
		out.Topology = topo
	}
	if len(child.AgentToolAccess) > 0 {
		out.AllowedAgents = append([]string(nil), child.AllowedAgents...)
		out.AgentToolAccess = cloneAgentToolAccess(child.AgentToolAccess)
	}
	if len(child.Rules) > 0 {
		out.Rules = mergeStringLists(parent.Rules, child.Rules)
	}
	if len(child.Gates) > 0 {
		out.Gates = append([]string(nil), child.Gates...)
	}
	if !child.Controls.IsZero() {
		out.Controls = mergeControls(out.Controls, cloneManifestControls(child.Controls))
	}
	if len(child.PhaseDefs) > 0 {
		out.PhaseDefs = mergePhaseDefs(out.PhaseDefs, clonePhaseDefs(child.PhaseDefs))
	}
	if len(child.Parameters) > 0 {
		if out.Parameters == nil {
			out.Parameters = map[string]WorkflowParameter{}
		}
		for name, spec := range child.Parameters {
			out.Parameters[name] = spec
		}
	}
	if child.Blueprint != nil {
		out.Blueprint = &BlueprintDef{
			ID: child.Blueprint.ID, Path: child.Blueprint.Path,
			Frontmatter: append([]string(nil), child.Blueprint.Frontmatter...),
		}
	}
	if len(child.Injects) > 0 {
		out.Injects = append(out.Injects, cloneWorkflowInjects(child.Injects)...)
	}
	if len(child.Presets) > 0 {
		out.Presets = cloneManifestPresets(child.Presets)
	}
	return out
}

func cloneManifest(in Manifest) Manifest {
	out := in
	out.Phases = append([]string(nil), in.Phases...)
	out.PhaseDefs = clonePhaseDefs(in.PhaseDefs)
	out.AllowedAgents = append([]string(nil), in.AllowedAgents...)
	out.AgentToolAccess = cloneAgentToolAccess(in.AgentToolAccess)
	out.Rules = append([]string(nil), in.Rules...)
	out.Gates = append([]string(nil), in.Gates...)
	out.Controls = cloneManifestControls(in.Controls)
	out.Parameters = cloneWorkflowParameters(in.Parameters)
	out.Presets = cloneManifestPresets(in.Presets)
	out.Injects = cloneWorkflowInjects(in.Injects)
	out.Featured = cloneBoolPointer(in.Featured)
	out.RequiresRepo = cloneBoolPointer(in.RequiresRepo)
	out.Request = cloneManifestRequest(in.Request)
	if in.Blueprint != nil {
		out.Blueprint = &BlueprintDef{
			ID: in.Blueprint.ID, Path: in.Blueprint.Path,
			Frontmatter: append([]string(nil), in.Blueprint.Frontmatter...),
		}
	}
	return out
}

func cloneBoolPointer(in *bool) *bool {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneWorkflowInjects(in []anchor.WorkflowInject) []anchor.WorkflowInject {
	if len(in) == 0 {
		return nil
	}
	out := make([]anchor.WorkflowInject, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Selector = cloneAnchorSelector(in[i].Selector)
		out[i].Dedup = cloneYAMLNode(in[i].Dedup)
	}
	return out
}

func cloneAnchorSelector(in anchor.Selector) anchor.Selector {
	out := in
	out.Phase = cloneStringPointer(in.Phase)
	out.Workflow = cloneStringPointer(in.Workflow)
	out.Tool = cloneStringPointer(in.Tool)
	out.Tools = append([]string(nil), in.Tools...)
	out.Profiles = append([]string(nil), in.Profiles...)
	out.Surfaces = append([]string(nil), in.Surfaces...)
	out.SessionPosture = append([]string(nil), in.SessionPosture...)
	return out
}

func cloneStringPointer(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneYAMLNode(in yaml.Node) yaml.Node {
	seen := map[*yaml.Node]*yaml.Node{}
	var clone func(*yaml.Node) *yaml.Node
	clone = func(node *yaml.Node) *yaml.Node {
		if node == nil {
			return nil
		}
		if copied, ok := seen[node]; ok {
			return copied
		}
		copied := *node
		seen[node] = &copied
		copied.Content = make([]*yaml.Node, len(node.Content))
		for i := range node.Content {
			copied.Content[i] = clone(node.Content[i])
		}
		copied.Alias = clone(node.Alias)
		return &copied
	}
	return *clone(&in)
}

func cloneManifestControls(in ManifestControls) ManifestControls {
	out := in
	if in.OnDecisionReject != nil {
		value := *in.OnDecisionReject
		out.OnDecisionReject = &value
	}
	if in.OnPause != nil {
		value := *in.OnPause
		out.OnPause = &value
	}
	if in.OnStop != nil {
		value := *in.OnStop
		out.OnStop = &value
	}
	if in.ContentReview != nil {
		out.ContentReview = &PhaseContentReview{
			Tools: append([]string(nil), in.ContentReview.Tools...),
			Paths: append([]string(nil), in.ContentReview.Paths...),
		}
	}
	if in.Report != nil {
		value := *in.Report
		value.Brief = in.Report.Brief.clone()
		out.Report = &value
	}
	return out
}

func clonePhaseDefs(in []PhaseDef) []PhaseDef {
	out := make([]PhaseDef, len(in))
	for i := range in {
		out[i] = in[i]
		if in[i].InvokeWorkflow != nil {
			value := *in[i].InvokeWorkflow
			out[i].InvokeWorkflow = &value
		}
		if in[i].OnEnter.RequestUserFeedback != nil {
			value := *in[i].OnEnter.RequestUserFeedback
			value.Options = append([]string(nil), value.Options...)
			value.ArtifactIDs = append([]string(nil), value.ArtifactIDs...)
			out[i].OnEnter.RequestUserFeedback = &value
		}
		out[i].OnEnter.Obligations = copyObligationDefs(in[i].OnEnter.Obligations)
		out[i].Gates = append([]string(nil), in[i].Gates...)
		out[i].ChildGates = append([]string(nil), in[i].ChildGates...)
		out[i].BindParallelGroup = append([]string(nil), in[i].BindParallelGroup...)
		out[i].TouchPaths = append([]string(nil), in[i].TouchPaths...)
		out[i].ModeRefs = append([]string(nil), in[i].ModeRefs...)
		out[i].Intake = append([]string(nil), in[i].Intake...)
		out[i].Transitions = copyPhaseTransitions(in[i].Transitions)
		if in[i].ContentReview != nil {
			out[i].ContentReview = &PhaseContentReview{
				Tools: append([]string(nil), in[i].ContentReview.Tools...),
				Paths: append([]string(nil), in[i].ContentReview.Paths...),
			}
		}
		if in[i].ParallelTask != nil {
			value := *in[i].ParallelTask
			out[i].ParallelTask = &value
		}
		if in[i].HumanApproval != nil {
			value := *in[i].HumanApproval
			out[i].HumanApproval = &value
		}
		if in[i].ReviewLoop != nil {
			out[i].ReviewLoop = cloneReviewLoop(in[i].ReviewLoop)
		}
	}
	return out
}

func cloneWorkflowParameters(in map[string]WorkflowParameter) map[string]WorkflowParameter {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]WorkflowParameter, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneManifestPresets(in []ManifestPreset) []ManifestPreset {
	if len(in) == 0 {
		return nil
	}
	out := make([]ManifestPreset, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Params = copyStringMap(in[i].Params)
	}
	return out
}

func cloneAgentToolAccess(in map[string]sandbox.ToolAccess) map[string]sandbox.ToolAccess {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]sandbox.ToolAccess, len(in))
	for id, access := range in {
		out[id] = access
	}
	return out
}

func mergeControls(parent, child ManifestControls) ManifestControls {
	out := parent
	if child.PhaseAdvance != "" {
		out.PhaseAdvance = child.PhaseAdvance
	}
	if child.DefaultExecutionMode != "" {
		out.DefaultExecutionMode = child.DefaultExecutionMode
	}
	if child.OnDecisionReject != nil {
		out.OnDecisionReject = child.OnDecisionReject
	}
	if child.OnPause != nil {
		out.OnPause = child.OnPause
	}
	if child.OnStop != nil {
		out.OnStop = child.OnStop
	}
	if child.ContentReview != nil {
		out.ContentReview = child.ContentReview
	}
	if child.Report != nil {
		out.Report = child.Report
	}
	return out
}

func mergeStringLists(parent, child []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range parent {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range child {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (c ManifestControls) IsZero() bool {
	return c.PhaseAdvance == "" && c.DefaultExecutionMode == "" &&
		c.OnDecisionReject == nil && c.OnPause == nil && c.OnStop == nil && c.ContentReview == nil &&
		c.Report == nil
}

func cloneReviewLoop(in *ReviewLoopDef) *ReviewLoopDef {
	value := *in
	value.VerdictSchema = copyStringMap(in.VerdictSchema)
	value.RequiredAgents = append([]string(nil), in.RequiredAgents...)
	value.IfSpawnable = append([]string(nil), in.IfSpawnable...)
	value.CoverageReviewers = append([]string(nil), in.CoverageReviewers...)
	value.ClaimStatuses = cloneClaimStatuses(in.ClaimStatuses)
	return &value
}
