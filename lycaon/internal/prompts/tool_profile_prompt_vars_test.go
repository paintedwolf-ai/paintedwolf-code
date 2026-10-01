package prompts_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestManagedSecretPartialTeachesReferenceOnlyWorkflowWhenCapabilityIsPresent(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := engine.Render(context.Background(), "units/managed-secret-tools.md", map[string]any{})
	testutil.FailErr(t, "render managed secret partial", err)
	for _, want := range []string{
		"Managed secrets", "Most authenticated work needs none", "{{paintedwolf-secret:…}}",
		"Default to chat scope", "Raw values never enter model requests",
		"tracked and replaced", "Destination approval",
		"Pass the returned reference unchanged", "Project files do not resolve references",
		"revoke disposable test references afterward",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("managed secret partial missing %q:\n%s", want, got)
		}
	}
	withAsk, err := engine.Render(context.Background(), "units/managed-secret-tools.md", map[string]any{
		"profile_has_ask_user": true,
	})
	testutil.FailErr(t, "render managed secret partial with ask_user", err)
	for _, want := range []string{"`ask_user`", "`response_type: secret`", "only the reference", "Never request credentials as text or choices"} {
		if !strings.Contains(withAsk, want) {
			t.Fatalf("managed secret ask guidance missing %q:\n%s", want, withAsk)
		}
	}
}

func TestCoordinatorTeachesSecretUseOnceSecretToolsLoad(t *testing.T) {
	vars := map[string]any{
		"has_file_tools": true,
		"agent_skills":   listedSkills("use-secrets-without-reading-them"),
	}
	loaded := map[string]bool{"secret_generate": true, "secret_list": true, "secret_revoke": true, "write": true}
	testutil.FailErr(t, "merge coordinator surface", prompts.MergeCoordinatorSurfacePathVars("implement_investigate", nil, vars, prompts.SurfaceTurn{Loaded: loaded}))
	sticky, err := prompts.LoadCoordinatorSurfaceFloor("implement_investigate")
	testutil.FailErr(t, "resolve coordinator tools", err)
	deferred, err := prompts.LoadCoordinatorSurfaceLoadable("implement_investigate")
	testutil.FailErr(t, "resolve deferred coordinator tools", err)
	if slices.Contains(sticky, "secret_generate") || !slices.Contains(deferred, "secret_generate") {
		t.Fatal("fixture must exercise deferred secret generation")
	}
	testutil.FailErr(t, "merge coordinator prompt", prompts.MergeCoordinatorPromptVars("implement_investigate",
		prompts.ExecutionModePromptTransition{ExecutionMode: "investigate"}, prompts.CoordinatorPromptGates{}, vars))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	mergeUnits(t, engine, vars, promptunit.HostCoordinator, "investigate", nil)
	out, err := engine.Render(context.Background(), "agents/coordinator-core.md", vars)
	testutil.FailErr(t, "render loaded secret guidance", err)
	for _, want := range []string{
		"Before handling a credential, read `use-secrets-without-reading-them`",
		"Pass the returned reference unchanged",
		"`response_type: secret`",
		"Raw values never enter model requests",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("first-turn secret teaching missing %q", want)
		}
	}
}

func TestHTTPRequestPartialTeachesFirstClassActionWorkflow(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := engine.Render(context.Background(), "units/http-request-tools.md", map[string]any{
		"profile_has_http_request": true,
		"profile_has_fetch_url":    true,
		"profile_has_wait":         true,
	})
	testutil.FailErr(t, "render HTTP request partial", err)
	for _, want := range []string{
		"HTTP actions", "Use `http_request` for APIs",
		"Use `fetch_url` for readable web research", "where `http_request` returns raw HTML",
		"Use `wait` with `http_ready`/`port_ready`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("HTTP request partial missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "curl") {
		t.Fatalf("HTTP request teaching must not advertise a shell client:\n%s", got)
	}

	if strings.Contains(got, "request_tools") {
		t.Fatalf("sticky HTTP tools must not be taught as deferred:\n%s", got)
	}
}

func TestHTTPActionVarsFollowOfferedTools(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	prompts.MergeHTTPActionVars([]string{"read", "wait", "http_request"}, vars)
	want := map[string]any{"profile_has_http_request": true, "profile_has_fetch_url": false, "profile_has_wait": true}
	if !reflect.DeepEqual(vars, want) {
		t.Fatalf("vars = %#v want %#v", vars, want)
	}
	prompts.MergeHTTPActionVars([]string{"fetch_url"}, vars)
	prompts.MergeWebResearchUnavailable(vars)
	if vars["profile_has_fetch_url"] != false {
		t.Fatalf("disabled web research still teaches fetch_url: %#v", vars)
	}
	absent := map[string]any{}
	prompts.MergeHTTPActionVars([]string{"read"}, absent)
	if absent["profile_has_http_request"] != false {
		t.Fatalf("absent tool advertised: %#v", absent)
	}
}

func loadProfileCapabilityVars(t *testing.T, profileID string) map[string]any {
	t.Helper()
	profile, err := prompts.ToolProfileForAgent(profileAgentForProfile(t, profileID))
	testutil.FailErr(t, "ToolProfileForAgent", err)
	if profile != profileID {
		t.Fatalf("profile %q != %q", profile, profileID)
	}
	into := map[string]any{}
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	err = prompts.MergeAgentToolSurfaceVars(profileID, nil, nil, prompts.SurfaceTurn{}, into, profiles)
	testutil.FailErr(t, "MergeAgentToolSurfaceVars", err)
	return into
}

func profileAgentForProfile(t *testing.T, profileID string) string {
	t.Helper()
	switch profileID {
	case "implement":
		return "implementer"
	case "explore_readonly":
		return "path-explorer"
	case "web_research":
		return "web-researcher"
	default:
		t.Fatalf("no fixture agent for profile %q", profileID)
		return ""
	}
}

func TestToolProfileCapabilityVarsImplement(t *testing.T) {
	vars := loadProfileCapabilityVars(t, "implement")
	if vars["profile_has_command"] != true {
		t.Fatalf("implement must have command: %#v", vars["profile_has_command"])
	}
	// Loadable tools are untaught until they load; the profile says more can load.
	if vars["profile_has_http_request"] != false || vars["profile_has_fetch_url"] != false || vars["more_tools_loadable"] != true {
		t.Fatalf("implement must leave loadable HTTP tools untaught until loaded: %#v", vars)
	}
	if vars["profile_has_managed_secrets"] != false {
		t.Fatalf("implement must not teach the managed-secret lifecycle before it loads: %#v", vars)
	}
	if vars["profile_has_read_tools"] != true {
		t.Fatalf("implement must advertise read tools")
	}
	// Isolated branches omit VCS metadata and commit tools.
	if vars["profile_has_git_commit"] == true {
		t.Fatalf("implement is isolated and must not have git_commit")
	}
	writeTools, _ := vars["tool_check_write_tools"].([]string)
	if !slices.Contains(writeTools, "edit") {
		t.Fatalf("write tools missing edit: %v", writeTools)
	}
	surveyTools, _ := vars["tool_check_survey_tools"].([]string)
	for _, want := range []string{"summarize", "survey_repo", "read", "grep", "find", "list_dir", "jq"} {
		if !slices.Contains(surveyTools, want) {
			t.Fatalf("survey tools missing %q: %v", want, surveyTools)
		}
	}
	concurrent, _ := vars["profile_concurrent_tools"].([]string)
	if !slices.Contains(concurrent, "grep") {
		t.Fatalf("grep should be concurrent: %v", concurrent)
	}
	if !slices.Contains(concurrent, "summarize") {
		t.Fatalf("summarize should be concurrent: %v", concurrent)
	}
	serial, _ := vars["profile_serial_tools"].([]string)
	if !slices.Contains(serial, "edit") || !slices.Contains(serial, "command") {
		t.Fatalf("serial tools missing edit/command: %v", serial)
	}
	if slices.Contains(serial, "summarize") {
		t.Fatalf("summarize must not be listed as serial mutation: %v", serial)
	}
	if vars["visual_show_available"] != true || vars["visual_show_terminal"] != true || vars["visual_show_page"] != false {
		t.Fatalf("implement must expose terminals and leave page capture untaught until loaded: %#v", vars)
	}
	if vars["profile_has_capture_page"] != false || vars["profile_has_page_open"] != false || vars["profile_has_terminal_snapshot"] != true {
		t.Fatalf("implement teaches eager terminal capture, not unloaded page tools: capture=%#v page_open=%#v terminal_snapshot=%#v",
			vars["profile_has_capture_page"], vars["profile_has_page_open"], vars["profile_has_terminal_snapshot"])
	}
	if vars["profile_has_terminal_capture"] != true {
		t.Fatal("implement command surface must advertise sealed terminal capture")
	}
}

func TestToolProfileCapabilityVarsExploreReadonly(t *testing.T) {
	vars := loadProfileCapabilityVars(t, "explore_readonly")
	if vars["profile_has_scan_drilldown"] != false || vars["more_tools_loadable"] != true {
		t.Fatal("unloaded scan tools stay untaught while the profile says more can load")
	}
	if required, _ := vars["tool_check_scan_drilldown_tools"].([]string); len(required) != 0 {
		t.Fatalf("deferred scans must not fail the eager schema check: %v", required)
	}
	if vars["profile_has_write_tools"] != false {
		t.Fatalf("explore_readonly must not advertise write tools")
	}
	if vars["profile_has_read_tools"] != true {
		t.Fatalf("explore_readonly must advertise read tools")
	}
	if vars["profile_mutation_capable"] != false {
		t.Fatalf("explore_readonly must not be mutation-capable")
	}
	if vars["profile_has_command"] != false {
		t.Fatalf("explore_readonly must not have command")
	}
	if vars["profile_has_http_request"] != false || vars["profile_has_fetch_url"] != false || vars["profile_has_wait"] != true {
		t.Fatalf("explore_readonly HTTP capability flags are wrong: %#v", vars)
	}
	if vars["profile_has_managed_secrets"] != false {
		t.Fatal("read-only profiles must not advertise managed-secret lifecycle tools")
	}
	if vars["profile_has_terminal_capture"] != false {
		t.Fatalf("explore_readonly must not advertise sealed terminal capture")
	}
	surveyTools, _ := vars["tool_check_survey_tools"].([]string)
	if slices.Contains(surveyTools, "write") || slices.Contains(surveyTools, "edit") {
		t.Fatalf("survey tools must be read-only: %v", surveyTools)
	}
	for _, want := range []string{"summarize", "survey_repo", "read", "grep", "find", "list_dir"} {
		if !slices.Contains(surveyTools, want) {
			t.Fatalf("explore_readonly survey tools missing %q: %v", want, surveyTools)
		}
	}
	verifyTools, _ := vars["tool_check_verify_tools"].([]string)
	if len(verifyTools) != 0 {
		t.Fatalf("explore_readonly must not list verify tools: %v", verifyTools)
	}
}

func TestAgentMutationCapableByCharacteristic(t *testing.T) {
	capable, ok := prompts.AgentMutationCapable("implementer")
	if !ok || !capable {
		t.Fatalf("implementer mutation capable = %v ok=%v", capable, ok)
	}
	capable, ok = prompts.AgentMutationCapable("repo-researcher")
	if !ok || capable {
		t.Fatalf("repo-researcher mutation capable = %v ok=%v", capable, ok)
	}
	capable, ok = prompts.AgentMutationCapable("implementer")
	if !ok || !capable {
		t.Fatalf("implementer mutation capable = %v ok=%v", capable, ok)
	}
}

func TestToolProfileMutationCapableMatchesAgentCharacteristic(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	if !prompts.ToolProfileMutationCapable(byID["implement"]) {
		t.Fatal("implement profile must be a write agent")
	}
	if prompts.ToolProfileMutationCapable(byID["explore_readonly"]) {
		t.Fatal("explore_readonly must not be a write agent")
	}
	if prompts.ToolProfileMutationCapable(sandbox.ToolProfile{}) {
		t.Fatal("empty profile must not be a write agent")
	}
}

func TestToolNamesMutationCapableUsesExecutionContract(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"write", "git_restore", "verify", "terminal_open"} {
		if !prompts.ToolNamesMutationCapable([]string{tool}) {
			t.Errorf("execution-contract mutation tool %q was classified read-only", tool)
		}
	}
	for _, tool := range []string{"read", "git_diff", "terminal_read", "secret_generate"} {
		if prompts.ToolNamesMutationCapable([]string{tool}) {
			t.Errorf("read/control tool %q was classified mutation-capable", tool)
		}
	}
}

func TestAgentSurveysProjectTreeByCharacteristic(t *testing.T) {
	surveys, ok := prompts.AgentSurveysProjectTree("path-explorer")
	if !ok || !surveys {
		t.Fatalf("path-explorer surveys = %v ok=%v", surveys, ok)
	}
	surveys, ok = prompts.AgentSurveysProjectTree("plan-reviewer")
	if !ok || !surveys {
		t.Fatalf("plan-reviewer surveys = %v ok=%v", surveys, ok)
	}
	surveys, ok = prompts.AgentSurveysProjectTree("web-researcher")
	if !ok || surveys {
		t.Fatalf("web-researcher surveys = %v ok=%v", surveys, ok)
	}
	surveys, ok = prompts.AgentSurveysProjectTree("implementer")
	if !ok || !surveys {
		t.Fatalf("implementer surveys = %v ok=%v (mutation gate is separate)", surveys, ok)
	}
}

func TestAgentIsReadScoutByArchetype(t *testing.T) {
	if !prompts.AgentIsReadScout("path-explorer") || !prompts.AgentIsReadScout("repo-researcher") {
		t.Fatal("explore_readonly archetype agents must be read scouts")
	}
	if prompts.AgentIsReadScout("code-reviewer") {
		t.Fatal("advisory_gate code-reviewer must not be a read scout")
	}
	if prompts.AgentIsReadScout("plan-reviewer") {
		t.Fatal("plan reviewers must not be read scouts")
	}
	if !prompts.AgentIsWebResearcher("web-researcher") {
		t.Fatal("web-researcher must resolve via web_research profile")
	}
}

func TestToolProfileCapabilityVarsImplementVerify(t *testing.T) {
	vars := loadProfileCapabilityVars(t, "implement")
	verifyTools, _ := vars["tool_check_verify_tools"].([]string)
	if !reflect.DeepEqual(verifyTools, []string{"command", "verify"}) {
		t.Fatalf("implement verify tools = %v want command+verify", verifyTools)
	}
}

func TestToolProfileCapabilityVarsWebResearch(t *testing.T) {
	vars := loadProfileCapabilityVars(t, "web_research")
	if vars["profile_has_grep"] != false {
		t.Fatalf("web_research must not have grep")
	}
	if vars["profile_has_read_tools"] != false {
		t.Fatalf("web_research must not advertise read tools")
	}
	mcp, _ := vars["tool_check_scan_drilldown_tools"].([]string)
	if !reflect.DeepEqual(mcp, []string{"scan_query"}) {
		t.Fatalf("web_research must expose only the advisory inventory query: %v", mcp)
	}
}

func TestVisibleToolsNativePartialVarsFromSurfaceTools(t *testing.T) {
	vars := prompts.VisibleToolsNativePartialVars([]string{"list_dir", "find", "grep", "read", "jq", "write"})
	if !vars["profile_has_list_dir"].(bool) {
		t.Fatalf("profile_has_list_dir = %v", vars["profile_has_list_dir"])
	}
	if !vars["profile_has_grep"].(bool) {
		t.Fatalf("profile_has_grep = %v", vars["profile_has_grep"])
	}
	if !vars["profile_has_jq"].(bool) {
		t.Fatalf("profile_has_jq = %v", vars["profile_has_jq"])
	}
	if !vars["has_find_tool"].(bool) {
		t.Fatalf("has_find_tool = %v", vars["has_find_tool"])
	}
	if !vars["profile_has_write_tools"].(bool) {
		t.Fatalf("profile_has_write_tools = %v", vars["profile_has_write_tools"])
	}
	if !vars["profile_has_read_tools"].(bool) {
		t.Fatalf("profile_has_read_tools = %v", vars["profile_has_read_tools"])
	}
	vars = prompts.VisibleToolsNativePartialVars([]string{"read", "grep", "survey_repo"})
	if !vars["profile_has_survey_repo"].(bool) {
		t.Fatalf("profile_has_survey_repo = %v", vars["profile_has_survey_repo"])
	}
	if !vars["profile_has_read_tools"].(bool) {
		t.Fatalf("profile_has_read_tools = %v", vars["profile_has_read_tools"])
	}
	vars = prompts.VisibleToolsNativePartialVars([]string{"list_dir", "find"})
	if vars["profile_has_read_tools"].(bool) {
		t.Fatalf("list_dir+find alone must not set profile_has_read_tools")
	}
	vars = prompts.VisibleToolsNativePartialVars([]string{"read", "grep"})
	if vars["profile_has_list_dir"].(bool) || vars["has_find_tool"].(bool) {
		t.Fatalf("unexpected list_dir/find flags for read+grep only: %+v", vars)
	}
	pageOnly := prompts.VisibleToolsNativePartialVars([]string{"capture_page", "page_open"})
	if _, ok := pageOnly["visual_show_available"]; ok {
		t.Fatal("on-wire capability vars must not write visual_show_*")
	}
}

func TestPreferNativeAndHostRunnerDerivedFromSurface(t *testing.T) {
	vars := prompts.VisibleToolsNativePartialVars([]string{"write", "command", "terminal_open"})
	prefer, _ := vars["prefer_native_file_tools"].([]string)
	host, _ := vars["host_runner_tools"].([]string)
	if !slices.Contains(prefer, "write") {
		t.Fatalf("prefer_native_file_tools missing write: %v", prefer)
	}
	if slices.Contains(prefer, "command") {
		t.Fatalf("prefer_native_file_tools must not include command: %v", prefer)
	}
	if !slices.Contains(host, "command") || !slices.Contains(host, "terminal_open") {
		t.Fatalf("host_runner_tools missing command/terminal_open: %v", host)
	}
	if slices.Contains(host, "write") {
		t.Fatalf("host_runner_tools must not include write: %v", host)
	}
}

func TestHostRunnerToolOrderIncludesNativeTerminalThenArgv(t *testing.T) {
	order := prompts.HostRunnerToolOrder()
	prefer := prompts.PreferNativeFileToolOrder()
	if len(prefer) == 0 {
		t.Fatal("PreferNativeFileToolOrder empty — native.filesystem failed to load")
	}
	if slices.Contains(prefer, "command") {
		t.Fatalf("command must not be in native.filesystem prefer order: %v", prefer)
	}
	if !slices.Contains(order, "command") || !slices.Contains(order, "verify") {
		t.Fatalf("HostRunnerToolOrder missing command/verify: %v", order)
	}
	commandIdx := slices.Index(order, "command")
	verifyIdx := slices.Index(order, "verify")
	termIdx := slices.Index(order, "terminal_open")
	if commandIdx < 0 || verifyIdx < 0 || termIdx < 0 {
		t.Fatalf("expected command/verify/terminal_open in %v", order)
	}
	if commandIdx > verifyIdx || verifyIdx > termIdx {
		t.Fatalf("want command, verify, then terminal tools; got %v", order)
	}
	if slices.Contains(order, "write") {
		t.Fatalf("write must not be in host runner order: %v", order)
	}
}
