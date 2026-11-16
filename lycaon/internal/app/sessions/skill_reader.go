package sessions

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func skillToolReader(ctx context.Context, mgr *session.Host, tctx tools.ToolContext) (prompts.Reader, error) {
	profileID := profileIDForSurface(tctx)
	surfaceID := strings.TrimSpace(tctx.Turn.TurnSurfaceID)
	if profileID != prompts.CoordinatorProfileID || !prompts.IsCoordinatorSurfaceID(surfaceID) {
		surfaceID = ""
	}
	var (
		profiles []sandbox.ToolProfile
		schemas  *toolschema.Config
	)
	if mgr != nil {
		if view := mgr.Catalog.ViewForSessionID(ctx, tctx.Identity.SessionID); view != nil {
			profiles = mgr.Profiles.ConstrainCommandProfiles(ctx, tctx.Identity.SessionID, view.ToolProfiles)
			schemas = view.ToolSchemas
		}
	}
	if len(profiles) == 0 {
		loaded, err := sandbox.LoadToolProfiles()
		if err != nil {
			return prompts.Reader{}, err
		}
		if mgr != nil {
			loaded = mgr.Profiles.ConstrainCommandProfiles(ctx, tctx.Identity.SessionID, loaded)
		}
		profiles = loaded
	}
	rootCount := len(tctx.Source.Roots)
	plan := tctx.Turn.TurnToolPlan
	if surfaceID != "" && !plan.Compiled() {
		compiled, err := prompts.CompileSurfacePlan(surfaceID, max(rootCount, 1))
		if err != nil {
			return prompts.Reader{}, err
		}
		plan = compiled
	}
	loaded := make(map[string]bool, len(tctx.Turn.TurnOfferedToolNames))
	for _, name := range tctx.Turn.TurnOfferedToolNames {
		if name = strings.TrimSpace(name); name != "" {
			loaded[name] = true
		}
	}
	if surfaceID != "" {
		for _, name := range plan.AddressableNames() {
			loaded[name] = true
		}
	}
	var visible []string
	if surfaceID == "" {
		visible = tctx.Turn.TurnOfferedToolNames
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:      profileID,
		SurfaceID:      surfaceID,
		Profiles:       profiles,
		Plan:           plan,
		Loaded:         loaded,
		Visible:        visible,
		Schemas:        schemas,
		RootCount:      rootCount,
		HasGit:         skillReaderHasGit(tctx),
		OnWorkerBranch: tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker && strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "",
		WebSearch:      true,
	})
}

func profileIDForSurface(tctx tools.ToolContext) string {
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		return mapped
	}
	return profileID
}

func skillReaderHasGit(tctx tools.ToolContext) bool {
	for _, dir := range []string{strings.TrimSpace(tctx.ActiveRootPath()), strings.TrimSpace(tctx.Source.WorkerBranchRoot)} {
		if dir == "" {
			continue
		}
		if _, ok := gitrepo.Discover(dir); ok {
			return true
		}
	}
	for _, r := range tctx.Source.Roots {
		if _, ok := gitrepo.Discover(strings.TrimSpace(r.Path)); ok {
			return true
		}
	}
	return false
}
