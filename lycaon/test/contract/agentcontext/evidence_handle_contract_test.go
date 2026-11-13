package contract

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// handleToken matches an evidence handle as copy writes it: kind#, kind#N.
var handleToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.-])([a-z][a-z0-9_]*)#`)

type toolState struct {
	name                 string
	offered, requestable []string
}

// Every evidence handle shared copy names must be one a tool reachable in that
// render can mint, so copy never sends a model to cite a kind it cannot hold.
func TestRenderedCopyNamesOnlyReachableEvidenceHandles(t *testing.T) {
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	kinds := binding.KindShapeMap()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	templates := sharedPromptTemplates(t)
	states := evidenceHandleStates(t, binding)
	for _, state := range states {
		reachable := reachableKinds(binding, manifest, append(slices.Clone(state.offered), state.requestable...))
		vars := prompts.VisibleToolsNativePartialVars(state.offered)
		prompts.MergeVisualShowVars(state.offered, state.requestable, vars)
		for _, name := range templates {
			rendered, err := engine.Render(t.Context(), name, maps.Clone(vars))
			contractcheck.FailErr(t, "render "+name+" for "+state.name, err)
			for _, match := range handleToken.FindAllStringSubmatch(rendered, -1) {
				kind := match[1]
				if _, known := kinds[kind]; !known {
					if binding.ToolKind(kind) != "" {
						t.Errorf("%s [%s]: names tool %q as an evidence handle; cite its kind %q", name, state.name, kind+"#", binding.ToolKind(kind)+"#")
					}
					continue
				}
				if !reachable[kind] {
					t.Errorf("%s [%s]: names %q, which no reachable tool mints (tools %v)", name, state.name, kind+"#", state.offered)
				}
			}
		}
	}
}

// Skill and policy copy is not rendered per tool set, so every handle it names
// must at least be a declared evidence kind.
func TestStaticCopyNamesDeclaredEvidenceHandles(t *testing.T) {
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	kinds := binding.KindShapeMap()
	packs := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs")
	var files []string
	for _, pattern := range []string{"*/*/skills/*/SKILL.md", "*/*/skills/*/references/*.md", "*/*/policy/*.yaml", "*/*/host/next-steps/*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(packs, pattern))
		contractcheck.FailErr(t, "glob "+pattern, err)
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no skill or policy copy found")
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, err)
		for _, match := range handleToken.FindAllStringSubmatch(string(body), -1) {
			kind := match[1]
			if _, known := kinds[kind]; !known && binding.ToolKind(kind) != "" {
				rel, _ := filepath.Rel(packs, path)
				t.Errorf("%s: names tool %q as an evidence handle; cite its kind %q", rel, kind+"#", binding.ToolKind(kind)+"#")
			}
		}
	}
}

// reachableKinds includes what command mints by running native operations,
// such as read# from cat.
func reachableKinds(binding *evidence.Binding, manifest nativemanifest.Config, tools []string) map[string]bool {
	out := map[string]bool{}
	for _, tool := range tools {
		names := []string{tool}
		if tool == "command" {
			for _, group := range manifest.Operations {
				names = append(names, group...)
			}
		}
		for _, name := range names {
			for _, kind := range binding.ProducibleKinds(name) {
				out[kind] = true
			}
		}
	}
	return out
}

func sharedPromptTemplates(t *testing.T) []string {
	t.Helper()
	shared := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "shared")
	var out []string
	for _, dir := range []string{"units", "partials"} {
		entries, err := os.ReadDir(filepath.Join(shared, dir))
		contractcheck.FailErr(t, "read "+dir, err)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				out = append(out, dir+"/"+entry.Name())
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no shared templates found")
	}
	return out
}

// evidenceHandleStates covers each evidence-producing tool alone, every
// coordinator surface, every tool profile, and all tools together.
func evidenceHandleStates(t *testing.T, binding *evidence.Binding) []toolState {
	t.Helper()
	all := binding.DeclaredToolNames()
	states := []toolState{{name: "none"}, {name: "all", offered: all}}
	for _, tool := range all {
		states = append(states, toolState{name: tool + " alone", offered: []string{tool}})
	}
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	for _, id := range slices.Sorted(maps.Keys(plans)) {
		states = append(states, toolState{name: "surface " + id, offered: plans[id].ImmediateNames(), requestable: plans[id].DeferredNames()})
	}
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, profile := range profiles {
		var offered, requestable []string
		for _, tool := range slices.Sorted(maps.Keys(profile.Tools)) {
			switch {
			case !profile.Tools[tool]:
			case profile.DeferredTools[tool]:
				requestable = append(requestable, tool)
			default:
				offered = append(offered, tool)
			}
		}
		states = append(states, toolState{name: "profile " + profile.ID, offered: offered, requestable: requestable})
	}
	return states
}
