package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestImplementerPersonaLoadableCatalog(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, orchestration.ProfileImplementer, nil)
	contractcheck.FailErr(t, "RenderPersona", err)

	for _, sub := range []string{"### More tools", "request_tools({\"need\"", "security scans", "repository investigation"} {
		if !strings.Contains(got, sub) {
			t.Fatalf("implementer persona missing %q", sub)
		}
	}
	// The map names capabilities; loadable tool names and descriptions stay out.
	for _, name := range []string{"### Loadable tools", "`source_history`", "`scan_list`", "`scan_query`", "`scan_summary`"} {
		if strings.Contains(got, name) {
			t.Fatalf("implementer persona lists a loadable tool: %s", name)
		}
	}
}

func TestProfileDeferredToolsGetRequestTools(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	for _, prof := range profiles {
		if len(prof.DeferredTools) > 0 {
			if !prof.Tools["request_tools"] {
				t.Fatalf("profile %s defers tools but request_tools was not injected", prof.ID)
			}
			if prof.ToolDeferred("request_tools") {
				t.Fatalf("profile %s: injected request_tools must be sticky", prof.ID)
			}
		}
		for name := range prof.DeferredTools {
			if !prof.Tools[name] {
				t.Fatalf("profile %s defers %s but does not allow it", prof.ID, name)
			}
		}
	}
}

func TestCoordinatorSurfaceFamiliesCompileComplete(t *testing.T) {
	t.Parallel()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surface catalog", err)
	for _, surfaceID := range catalog.SurfaceIDs() {
		declared, rowErr := catalog.Surface(surfaceID)
		contractcheck.FailErr(t, "load surface "+surfaceID, rowErr)
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		contractcheck.FailErr(t, "CompileToolPlan "+surfaceID, err)
		compiled := plan.ImmediateNames()
		compiledSet := make(map[string]struct{}, len(compiled))
		for _, name := range compiled {
			compiledSet[name] = struct{}{}
		}
		for _, name := range declared.Floor {
			family, ok := toolcontract.FamilyOf(name)
			if !ok {
				continue
			}
			for _, member := range toolcontract.ExpandFamilies([]string{name}) {
				if _, ok := compiledSet[member]; !ok {
					t.Fatalf("surface %s lists %s but compile omitted family %s member %s", surfaceID, name, family, member)
				}
			}
		}
	}
}

func TestCoordinatorResourcePresenceImpliesCommandFamily(t *testing.T) {
	t.Parallel()
	compiled := toolcontract.AppendImplied(nil, toolcontract.ResourcePresence{CommandJobs: true})
	got := make(map[string]struct{}, len(compiled))
	for _, name := range compiled {
		got[name] = struct{}{}
	}
	for _, name := range []string{"command_output", "command_stop"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("live command jobs must imply %s; got %v", name, compiled)
		}
	}
	if _, ok := got["command"]; ok {
		t.Fatalf("live command jobs must not imply command: %v", compiled)
	}
}

func TestCommandFamilyGrantedWhereverCommandIsGranted(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	granted := 0
	for _, prof := range profiles {
		if !prof.Tools["command"] || prof.ToolDenied("command") {
			continue
		}
		granted++
		for _, name := range []string{"command_output", "command_stop"} {
			if !prof.Tools[name] {
				t.Errorf("profile %s grants command but does not grant %s", prof.ID, name)
				continue
			}
			if prof.ID == "coordinator" {
				continue
			}
			if prof.ToolDeferred(name) {
				t.Errorf("profile %s grants command but defers %s", prof.ID, name)
			}
		}
	}
	if granted == 0 {
		t.Fatal("no profile grants command")
	}
}

func TestSkillsReadNeverDeferred(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	granted := 0
	for _, prof := range profiles {
		if !prof.Tools["skills_read"] {
			continue
		}
		granted++
		if prof.ToolDeferred("skills_read") {
			t.Errorf("profile %s grants skills_read but defers it", prof.ID)
		}
	}
	if granted == 0 {
		t.Fatal("no profile grants skills_read")
	}
}

func TestCoordinatorProfileDefersMCPWildcards(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	for _, prof := range profiles {
		if prof.ID != "coordinator" {
			continue
		}
		if !prof.ToolDeferred("mcp_coropa_intel_search") {
			t.Fatal("coordinator must defer mcp_* behind request_tools")
		}
		return
	}
	t.Fatal("coordinator profile missing")
}

func TestWorkerProfilesNeverPinMCPWildcardsSticky(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	for _, prof := range profiles {
		if prof.ID == "coordinator" {
			continue
		}
		for name := range prof.StickyTools {
			if strings.HasPrefix(name, "mcp_") && strings.HasSuffix(name, "*") {
				t.Fatalf("profile %s pins wildcard %s sticky", prof.ID, name)
			}
		}
	}
}

func TestWorkerProfilesKeepCoreEagerAndOptionalCapabilitiesReachable(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load worker profiles", err)
	cases := map[string]struct{ eager, deferred, denied []string }{
		"implement":        {eager: []string{"read", "write", "edit", "command", "command_output", "command_stop", "verify", "complete_leg", "request_tools", "skills_read", "terminal_open", "terminal_close"}, deferred: []string{"page_open", "page_act", "page_close", "capture_page", "http_request", "scan_pack", "secret_generate", "secret_list", "secret_revoke", "handoff_reserve"}, denied: []string{"delegate_start", "state_set"}},
		"explore_readonly": {eager: []string{"read", "grep", "find", "summarize", "git_status", "git_diff", "record_finding", "complete_leg", "request_tools", "skills_read"}, deferred: []string{"page_open", "page_act", "page_close", "capture_page", "scan_query", "git_log", "git_show"}, denied: []string{"command", "write", "git_commit", "secret_generate"}},
	}
	for _, profile := range profiles {
		spec, ok := cases[profile.ID]
		if !ok {
			continue
		}
		for _, name := range spec.eager {
			if !profile.ToolAllowed(name) || !profile.ToolSticky(name) {
				t.Errorf("%s core %s not eager", profile.ID, name)
			}
		}
		for _, name := range spec.deferred {
			if !profile.ToolAllowed(name) || !profile.ToolDeferred(name) {
				t.Errorf("%s optional %s not requestable", profile.ID, name)
			}
		}
		for _, name := range spec.denied {
			if profile.ToolAllowed(name) {
				t.Errorf("%s widened to %s", profile.ID, name)
			}
		}
		delete(cases, profile.ID)
	}
	if len(cases) > 0 {
		t.Fatalf("profiles missing: %v", cases)
	}
}
