package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptDumpOptions selects annotated coordinator prompt rendering for debug.
type PromptDumpOptions struct {
	FixtureName    string
	SessionID      string
	PreviousFamily string
	Causes         []surface.ModeTransitionCause
	UserPrompt     string
	// VerifyRequired simulates a workflow phase declaring evidence_passed:verify,
	// so the dump shows the opt-in verify protocol copy.
	VerifyRequired bool
}

// PromptDumpLine is one annotated section in a prompt dump.
type PromptDumpLine struct {
	Label   string
	Content string
}

// RenderPromptDump produces an annotated markdown trace of steady + transition layers.
func RenderPromptDump(
	ctx context.Context,
	engine prompts.PromptTemplateEngine,
	configRoot string,
	opts PromptDumpOptions,
) (string, error) {
	if engine == nil {
		return "", fmt.Errorf("prompt engine required")
	}
	sess := &api.Session{Posture: api.SessionPostureBuild, WorkspacePath: configRoot}
	if opts.SessionID != "" {
		sess.ID = opts.SessionID
	}
	userPrompt := strings.TrimSpace(opts.UserPrompt)
	if userPrompt == "" {
		userPrompt = "fix the auth bug in src/auth.go"
	}
	frame := inject.CoordinatorTurnFrame{}
	if opts.VerifyRequired {
		frame.EvidenceRequirements = []string{"verify"}
	}
	history := []api.Message{{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript,
		Content:    userPrompt,
	}}
	implState := surface.ImplementSessionState{}
	profile := tripartiteFixtureProfile(opts.FixtureName, frame.RunContext, sess, history, implState)
	previous := strings.TrimSpace(opts.PreviousFamily)
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	transition := surface.ComputeModeTransition(previous, current, opts.Causes)

	base := map[string]any{"project_dir": sess.WorkspacePath}
	var roots []projectroot.RootRef
	if sess.WorkspacePath != "" {
		roots = []projectroot.RootRef{{Path: sess.WorkspacePath, Label: "Fixture", IsPrimary: true}}
	}
	prompts.MergeWorkspaceRootsVars(base, roots, sess.WorkspacePath)
	rootCount, _ := base["root_count"].(int)
	roster := inject.ResolveAgentRoster(profile.SurfaceID, spawn.AmbientAllowedAgents(), rootCount, false, true)
	if err := capability.MergeForTurn(base, profile, rootCount, roster.Effective, true); err != nil {
		return "", fmt.Errorf("merge fixture capabilities: %w", err)
	}
	for key, value := range spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget()) {
		base[key] = value
	}
	if configRoot != "" {
		if err := prompts.MergeCoordinatorSurfacePathVars(profile.SurfaceID, nil, base, prompts.SurfaceTurn{}); err != nil {
			return "", err
		}
	}
	if err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionMode:         transition.ExecutionMode,
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		prompts.CoordinatorPromptGates{
			PendingOverlayPromote: len(implState.PendingOverlayIDs) > 0,
			VerifyRequired:        frame.RequiresEvidence("verify"),
			VerifyCommand:         implState.VerifyCommand,
		},
		base,
	); err != nil {
		return "", fmt.Errorf("merge coordinator prompt vars: %w", err)
	}

	// The fixture is the widest prompt the surface can render: every loadable
	// tool counts as offered, so every unit that could join the turn does.
	floor, _ := base["surface_offered"].([]string)
	loadable, err := prompts.LoadCoordinatorSurfaceLoadable(profile.SurfaceID)
	if err != nil {
		return "", fmt.Errorf("surface loadable: %w", err)
	}
	offered := append(append([]string(nil), floor...), loadable...)
	unitCatalog, err := prompts.UnitCatalogForEngine(engine)
	if err != nil {
		return "", fmt.Errorf("unit catalog: %w", err)
	}
	sel := prompts.UnitSelectionVars(promptunit.HostCoordinator, current, floor, offered, nil)
	blocks, err := prompts.RenderUnitSlots(ctx, prompts.UnitRendererFor(engine), unitCatalog, sel, base)
	if err != nil {
		return "", fmt.Errorf("render units: %w", err)
	}
	prompts.MergeUnitVars(base, blocks)

	var sections []PromptDumpLine
	appendRender := func(label, ref string) error {
		chunk, err := engine.Render(ctx, ref, base)
		if err != nil {
			return fmt.Errorf("render %s: %w", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			sections = append(sections, PromptDumpLine{Label: label, Content: trimmed})
		}
		return nil
	}
	if err := appendRender("core", surface.CoordinatorCoreTemplate); err != nil {
		return "", err
	}
	for _, modeRef := range profile.ModeRefs {
		if err := appendRender("mode:"+modeRef, surface.ModeTemplateRef(modeRef)); err != nil {
			return "", err
		}
	}
	if profile.SurfaceTemplate != "" {
		if err := appendRender("surface:"+profile.SurfaceID, profile.SurfaceTemplate); err != nil {
			return "", err
		}
	}
	if inject.ShouldRenderTransitionInject(transition) {
		stem, err := anchor.ResolveInformRender(ctx, anchor.InjectTransition, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID})
		if err != nil {
			return "", err
		}
		if err := appendRender("transition", "inject/"+stem+".md"); err != nil {
			return "", err
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Coordinator prompt dump\n\n")
	b.WriteString("Synthetic layer trace with stock capabilities and web research enabled; excludes device/project skills, host context, and tool schemas. Use logs prompt for a captured model request.\n\n")
	fmt.Fprintf(&b, "- surface_id: `%s`\n", profile.SurfaceID)
	fmt.Fprintf(&b, "- execution_mode: `%s`\n", transition.ExecutionMode)
	fmt.Fprintf(&b, "- execution_mode_previous: `%s`\n", transition.ExecutionModePrevious)
	fmt.Fprintf(&b, "- execution_mode_entered: `%s`\n", transition.ExecutionModeEntered)
	fmt.Fprintf(&b, "- execution_mode_left: `%s`\n", transition.ExecutionModeLeft)
	if len(opts.Causes) > 0 {
		fmt.Fprintf(&b, "- pushed_cause: `%s` mode=`%s`\n", opts.Causes[0].Kind, opts.Causes[0].Mode)
	}
	if opts.SessionID != "" {
		fmt.Fprintf(&b, "- session_id: `%s`\n", opts.SessionID)
	}
	if opts.FixtureName != "" {
		fmt.Fprintf(&b, "- fixture: `%s`\n", opts.FixtureName)
	}
	fmt.Fprintf(&b, "- verify_required: `%v`\n", opts.VerifyRequired)
	b.WriteString("\n")
	for _, sec := range sections {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", sec.Label, sec.Content)
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

func tripartiteFixtureProfile(
	name string,
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	history []api.Message,
	implState surface.ImplementSessionState,
) surface.TurnProfile {
	switch strings.TrimSpace(name) {
	case "implement_investigate_idle":
		return surface.ResolveTurnProfileForSurface("implement_investigate", runCtx, sess)
	case "implement_synthesis_first_user":
		return surface.ResolveTurnProfileForSurface("implement_synthesis", runCtx, sess)
	case "implement_dispatch_implementer_chain":
		return surface.ResolveTurnProfileForSurface(surface.SurfaceImplementDispatch, runCtx, sess)
	default:
		return surface.ResolveTurnProfile(runCtx, sess, history, implState)
	}
}
