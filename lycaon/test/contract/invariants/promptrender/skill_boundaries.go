package promptrender

import (
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

type skillBoundaryAuth struct {
	label      string
	profileID  string
	mode       api.SessionMode
	posture    api.SessionPosture
	onlySkills []skills.Skill
	auth       hoststate.TurnAuthority
}

func profileHasProcessExecSurface(
	profiles []sandbox.ToolProfile,
	profileID string,
	sessionMode api.SessionMode,
	posture api.SessionPosture,
) bool {
	if sessionMode == api.SessionModeAsk {
		return false
	}
	if posture == "" {
		posture = api.SessionPostureBuild
	}
	narrowed, err := hoststate.NarrowProfilesForPosture(profiles, posture, false)
	if err != nil {
		return false
	}
	profileID = strings.TrimSpace(profileID)
	extExec, _ := toolcontract.NewCommandEffects(toolcontract.CommandExternalExec)
	for _, p := range narrowed {
		if p.ID != profileID {
			continue
		}
		if !p.CommandEffects.Allows(extExec) {
			return false
		}
		commandExec := p.ToolAllowed("command") && (p.CommandEffects == 0 || p.CommandEffects.Allows(extExec))
		return commandExec || p.ToolAllowed("terminal_open")
	}
	return false
}

// skillBoundaryAuthorities constructs reachable single-axis reader projections
// so every stock skill template branch has both boolean witnesses verified for
// claim accuracy.
func skillBoundaryAuthorities(stock *Stock, coordPairs []coordRep, loaded []skills.Skill) ([]skillBoundaryAuth, error) {
	var base hoststate.TurnAuthority
	for _, cp := range coordPairs {
		if cp.auth.ProgramTable != nil && cp.state.Posture == api.SessionPostureBuild && cp.state.RootCount == 1 {
			base = cp.auth
			break
		}
	}
	roProfiles, err := hoststate.NarrowProfilesForPosture(stock.Profiles, api.SessionPostureSpec, false)
	if err != nil {
		return nil, err
	}
	extReadEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandExternalExec)
	if err != nil {
		return nil, err
	}
	extReadProfiles := slices.Clone(stock.Profiles)
	for i := range extReadProfiles {
		extReadProfiles[i].CommandEffects = extReadEffects
	}
	skillsByName := func(names ...string) []skills.Skill {
		if len(names) == 0 {
			return nil
		}
		var out []skills.Skill
		for _, sk := range loaded {
			if slices.Contains(names, sk.Name) {
				out = append(out, sk)
			}
		}
		return out
	}

	makeAuth := func(
		label, profileID string,
		posture api.SessionPosture,
		profiles []sandbox.ToolProfile,
		offered, deferred []string,
		schemaOverrides map[string]map[string]any,
		onlySkills []skills.Skill,
		rootCount int,
		hasGit, onWorkerBranch, webSearch bool,
		scratchDir string,
	) (skillBoundaryAuth, error) {
		sortedOff := slices.Clone(offered)
		slices.Sort(sortedOff)
		schemas := make(map[string]map[string]any, len(sortedOff))
		metas := make([]tools.ToolMeta, 0, len(sortedOff))
		loadedMap := make(map[string]bool, len(sortedOff))
		for _, name := range sortedOff {
			loadedMap[name] = true
			meta, ok := stock.Schemas.ToolMeta(name)
			if !ok {
				continue
			}
			sch := meta.ArgsSchema
			if ov, hasOv := schemaOverrides[name]; hasOv {
				sch = ov
			}
			schemas[name] = sch
			metas = append(metas, tools.ToolMeta{Name: name, Description: meta.Description, ArgsSchema: sch})
		}
		postSchemas := make(map[string]map[string]any, len(deferred))
		for _, name := range deferred {
			if meta, ok := stock.Schemas.ToolMeta(name); ok {
				postSchemas[name] = meta.ArgsSchema
			}
		}
		plan := toolsurface.Compile(sortedOff, deferred)
		visible := append(slices.Clone(sortedOff), deferred...)
		r, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID:         profileID,
			SessionMode:       string(api.SessionModeAgent),
			Profiles:          profiles,
			Plan:              plan,
			Loaded:            loadedMap,
			Visible:           visible,
			Schemas:           stock.Schemas,
			ProjectedSchemas:  schemas,
			RootCount:         rootCount,
			HasGit:            hasGit,
			HasAnyGitRoot:     hasGit || rootCount > 1,
			OnWorkerBranch:    onWorkerBranch,
			WebSearch:         webSearch,
			SessionScratchDir: scratchDir,
		})
		if rErr != nil {
			return skillBoundaryAuth{}, rErr
		}
		mergedSchemas := make(map[string]map[string]any, len(schemas)+len(postSchemas))
		maps.Copy(mergedSchemas, postSchemas)
		maps.Copy(mergedSchemas, schemas)
		reach := toolreach.Freeze(sortedOff, plan).WithSchemas(mergedSchemas)
		if base.ProgramTable != nil {
			reach = reach.WithProgramTable(base.ProgramTable)
		}
		return skillBoundaryAuth{
			label:      "skill-boundary:" + label,
			profileID:  profileID,
			mode:       api.SessionModeAgent,
			posture:    posture,
			onlySkills: onlySkills,
			auth: hoststate.TurnAuthority{
				Metas:           metas,
				Schemas:         schemas,
				PostLoadSchemas: postSchemas,
				Offered:         sortedOff,
				Plan:            plan,
				ProgramReach:    base.ProgramReach,
				ProgramTable:    base.ProgramTable,
				Reach:           reach,
				Reader:          r,
				Digest:          "skill-boundary:" + label,
			},
		}, nil
	}

	narrowContainerCmd := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":      map[string]any{"type": "string"},
			"verification": map[string]any{"type": "boolean"},
			"capability_request": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host_resources":   map[string]any{"type": "array"},
					"credential_store": map[string]any{"type": "boolean"},
					"local_listen":     map[string]any{"type": "object"},
					"loopback_connect": map[string]any{"type": "object"},
				},
			},
		},
	}
	cmdMeta, _ := stock.Schemas.ToolMeta("command")
	narrowRoCmd := toolschema.ProjectToolArgsSchema("command", cmdMeta.ArgsSchema, toolschema.ProjectOptions{CommandReadOnly: true})

	var out []skillBoundaryAuth
	specs := []struct {
		label, profileID       string
		posture                api.SessionPosture
		profiles               []sandbox.ToolProfile
		offered, deferred      []string
		overrides              map[string]map[string]any
		onlySkills             []skills.Skill
		roots                  int
		git, workerBranch, web bool
		scratch                string
	}{
		{"deferred-command-with-verify", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"request_tools", "verify", "recall", "skills_read"}, []string{"command"}, nil, skillsByName("prepare-a-release"), 1, true, false, true, "/tmp/pw-scratch"},
		{"verify-only", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"verify", "recall"}, nil, nil, skillsByName("prepare-a-release"), 1, true, false, false, ""},
		{"readonly-worker-branch", "implement", api.SessionPostureSpec, roProfiles, []string{"command", "recall", "complete_leg"}, nil, map[string]map[string]any{"command": narrowRoCmd}, nil, 1, true, true, false, ""},
		{"web-search-without-fetch", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"web_search", "recall"}, nil, nil, nil, 1, true, false, true, ""},
		{"zero-root-command-and-wait", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"command", "verify", "wait"}, nil, nil, nil, 0, false, false, false, ""},
		{"scan-summary-without-query", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"scan_summary", "recall"}, nil, nil, nil, 1, true, false, false, ""},
		{"terminal-open-without-command", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"terminal_open", "secret_generate", "secret_list", "ask_user"}, nil, nil, skillsByName("use-secrets-without-reading-them"), 1, true, false, false, ""},
		{"browser-without-command-stop", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"command", "page_open", "page_snapshot", "page_act", "page_close", "capture_page", "measure_page", "render_view", "view_image", "view_video", "wait"}, nil, nil, nil, 1, true, false, false, ""},
		{"container-cmd-no-socks-or-writeroot", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"command", "recall"}, nil, map[string]map[string]any{"command": narrowContainerCmd}, skillsByName("work-with-containers"), 1, true, false, false, ""},
		{"task-without-instruct-worker", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"task", "recall", "update_progress"}, []string{"instruct_worker"}, nil, nil, 1, true, false, false, ""},
		{"source-history-with-deferred-command", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"source_history", "request_tools", "recall"}, []string{"command"}, nil, nil, 1, true, false, false, ""},
		{"source-history-deferred-command-mixed-git", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"source_history", "request_tools", "recall"}, []string{"command"}, nil, skillsByName("investigate-code-history"), 2, false, false, false, ""},
		{"readonly-non-git-command-without-source-history", "implement", api.SessionPostureSpec, roProfiles, []string{"command", "recall", "complete_leg"}, nil, map[string]map[string]any{"command": narrowRoCmd}, nil, 1, false, false, false, ""},
		{"survey-repo-without-command", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"survey_repo", "summarize", "recall"}, nil, nil, skillsByName("survey-a-threat-model"), 1, true, false, false, ""},
		{"readonly-container-with-external-exec", prompts.CoordinatorProfileID, api.SessionPostureBuild, extReadProfiles, []string{"command", "recall"}, nil, nil, skillsByName("work-with-containers"), 1, true, false, false, ""},
		{"deferred-command-git-and-verify", prompts.CoordinatorProfileID, api.SessionPostureBuild, stock.Profiles, []string{"request_tools", "recall", "skills_read", "read", "grep", "glob"}, []string{"command"}, nil, skillsByName("commit-in-logical-groups", "hunt-a-regression-with-bisect", "diagnose-a-tls-failure"), 1, true, false, false, ""},
	}
	for _, sp := range specs {
		sb, bErr := makeAuth(sp.label, sp.profileID, sp.posture, sp.profiles, sp.offered, sp.deferred, sp.overrides, sp.onlySkills, sp.roots, sp.git, sp.workerBranch, sp.web, sp.scratch)
		if bErr != nil {
			return nil, bErr
		}
		out = append(out, sb)
	}
	return out, nil
}
