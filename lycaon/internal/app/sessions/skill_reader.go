package sessions

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
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
		sess     *api.Session
	)
	if mgr != nil {
		if mgr.Chats != nil && strings.TrimSpace(tctx.Identity.SessionID) != "" {
			sess, _ = mgr.Chats.Get(ctx, tctx.Identity.SessionID)
		}
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
		if mgr != nil && mgr.Profiles != nil {
			loaded = mgr.Profiles.ConstrainCommandProfiles(ctx, tctx.Identity.SessionID, loaded)
		}
		profiles = loaded
	}
	mode := api.SessionModeAgent
	if tctx.Source.AskMode || (sess != nil && sess.Mode == api.SessionModeAsk) {
		mode = api.SessionModeAsk
	}
	rootCount := len(tctx.Source.Roots)
	plan := tctx.Turn.TurnToolPlan
	if surfaceID != "" && !plan.Compiled() {
		compiled, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID, SessionMode: mode}, rootCount)
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
	if tctx.Turn.TurnOfferedToolNames == nil && mgr != nil && mgr.Coordinator != nil && mgr.Coordinator.Loading != nil && sess != nil {
		for name, ok := range mgr.Coordinator.Loading.LoadedTools(sess.ID) {
			if ok {
				loaded[name] = true
			}
		}
	}
	var visible []string
	if tctx.Turn.TurnOfferedToolNames != nil {
		visible = tctx.Turn.TurnOfferedToolNames
	}
	webSearch := true
	if mgr != nil && mgr.Coordinator != nil && mgr.Coordinator.Context != nil {
		webSearch = mgr.Coordinator.Context.WebSearchEnabled()
	}
	var (
		reviewsConfinedWrites bool
		externalHidden        bool
		sharedTemp            []string
		spawnAllowlist        []string
	)
	if mgr != nil && mgr.Profiles != nil {
		externalHidden = mgr.Profiles.CommandExternalHidden()
		if sess != nil {
			reviewsConfinedWrites = mgr.Profiles.ProjectApprovalPosture(ctx, sess).ReviewsConfinedWrites()
			sharedTemp = mgr.Profiles.WritableSharedTemp(ctx, sess)
		}
	}
	if profileID == prompts.CoordinatorProfileID {
		spawnAllowlist = spawn.AmbientAllowedAgents()
		if mgr != nil && mgr.Coordinator != nil && mgr.Coordinator.Context != nil && sess != nil {
			if runCtx, err := mgr.Coordinator.Context.RunContext(ctx, sess.ID); err == nil && len(runCtx.AllowedAgents) > 0 {
				spawnAllowlist = runCtx.AllowedAgents
			}
		}
	}
	scratchDir := strings.TrimSpace(tctx.Host.SessionScratchDir)
	if scratchDir == "" && mgr != nil && mgr.ToolContext != nil && sess != nil {
		scratchDir = mgr.ToolContext.ScratchDir(ctx, sess)
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SurfaceID:             surfaceID,
		SessionMode:           string(mode),
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                loaded,
		Visible:               visible,
		Schemas:               schemas,
		ProjectedSchemas:      tctx.Turn.TurnOfferedToolSchemas,
		RootCount:             rootCount,
		HasGit:                primaryRootHasGit(tctx),
		HasAnyGitRoot:         anyRootHasGit(tctx),
		OnWorkerBranch:        tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker && strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "",
		WebSearch:             webSearch,
		SpawnAllowlist:        spawnAllowlist,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: externalHidden,
		SessionScratchDir:     scratchDir,
		SharedTempLocations:   sharedTemp,
	})
}

func profileIDForSurface(tctx tools.ToolContext) string {
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		return mapped
	}
	return profileID
}

func primaryRootHasGit(tctx tools.ToolContext) bool {
	primaryPath := strings.TrimSpace(tctx.ActiveRootPath())
	if primaryPath == "" && len(tctx.Source.Roots) > 0 {
		if r, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
			primaryPath = strings.TrimSpace(r.Path)
		}
	}
	if primaryPath != "" {
		if _, ok := gitrepo.Discover(primaryPath); ok {
			return true
		}
	}
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		if _, ok := gitrepo.Discover(branch); ok {
			return true
		}
	}
	return false
}

func anyRootHasGit(tctx tools.ToolContext) bool {
	if primaryRootHasGit(tctx) {
		return true
	}
	for _, r := range tctx.Source.Roots {
		if path := strings.TrimSpace(r.Path); path != "" {
			if _, ok := gitrepo.Discover(path); ok {
				return true
			}
		}
	}
	return false
}
