package contract

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// Invariant: pre-invoke pipeline stages read caller arguments without mutating them.
func TestPreInvokePipelineNeverMutatesCallerArguments(t *testing.T) {
	t.Parallel()
	executor, defs := stubbedContractExecutor(t)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native tool manifest", err)
	commandScoped := map[string]bool{}
	for _, name := range manifest.CommandScopedTools() {
		commandScoped[name] = true
	}

	root := t.TempDir()
	seedCommandTree(t, root)
	var mutated, unstable []string
	for _, def := range defs {
		name := def.Meta.Name
		samples := schemaSamples(def.Meta.ArgsSchema)
		if commandScoped[name] {
			for _, tc := range commandPlanCorpus() {
				samples = append(samples, tc.args)
			}
		}
		ctx := tools.ToolContext{
			Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root", ProjectID: "project", SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			SessionID: "chat", ToolCallID: "call-" + name, Agent: profileAllowing(profiles, name),
		}
		for _, sample := range samples {
			args := cloneArgs(sample).(map[string]any)
			_, firstErr := invokeQuietly(t.Context(), executor, name, args, ctx)
			if !argsUnchanged(sample, args) {
				mutated = append(mutated, fmt.Sprintf("%s: %v became %v", name, sample, args))
				continue
			}
			_, secondErr := invokeQuietly(t.Context(), executor, name, args, ctx)
			if outcomeCode(firstErr) != outcomeCode(secondErr) {
				unstable = append(unstable, fmt.Sprintf("%s %v: first %s, second %s", name, sample, outcomeCode(firstErr), outcomeCode(secondErr)))
			}
		}
	}
	if len(mutated) > 0 {
		sort.Strings(mutated)
		t.Errorf("the pre-invoke pipeline wrote into the caller's arguments; derive new values into a copy "+
			"(or ToolContext) and leave args as the model sent them:\n  %s", strings.Join(mutated, "\n  "))
	}
	if len(unstable) > 0 {
		sort.Strings(unstable)
		t.Errorf("the same arguments planned twice reached different outcomes; planning must be a pure function of the arguments:\n  %s",
			strings.Join(unstable, "\n  "))
	}
}

// stubbedContractExecutor is the contract executor with every boot-registered
// tool's handler replaced by a no-op, so only the pre-invoke pipeline runs.
func stubbedContractExecutor(t *testing.T) (*toolexecution.Executor, []tools.Definition) {
	t.Helper()
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
		ConfigRoot: filepath.Join(contractcheck.RepoRoot(t), "lycaon"),
		Catalog:    contractcheck.StockCatalog(t),
	})
	contractcheck.FailErr(t, "toolhost.NewRuntime", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load effective tool schemas", err)
	rt.Executor.Metadata.SetToolSchemas(schemas)
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	toolfixture.WireContractBlockPlane(t, rt, guidance.NewStaticRejectFormatter(hints))

	boot := toolfixture.ContractServeBootRegistry(t)
	var defs []tools.Definition
	for _, meta := range boot.Metadata.List() {
		def, ok := boot.Definition(meta.Name)
		if !ok {
			continue
		}
		stub := tools.Definition{Meta: def.Meta, Contract: def.Contract, Handler: noopHandler}
		contractcheck.FailErr(t, "register stub "+meta.Name, rt.Registry.RegisterDefinition(stub))
		defs = append(defs, stub)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Meta.Name < defs[j].Meta.Name })
	if len(defs) < 50 {
		t.Fatalf("the boot registry registered %d tools; the fixture no longer mirrors serve boot", len(defs))
	}
	return rt.Executor, defs
}

func noopHandler(context.Context, map[string]any, tools.ToolContext) (string, error) {
	return `{"ok":true}`, nil
}

// invokeQuietly reports a handler-free invocation's outcome; a panic is an outcome too.
func invokeQuietly(ctx context.Context, executor *toolexecution.Executor, name string, args map[string]any, tc tools.ToolContext) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return executor.Invoke(ctx, name, args, tc)
}

// outcomeCode is the structured identity of an outcome: ok, a reject code, or
// an unstructured error.
func outcomeCode(err error) string {
	if err == nil {
		return "ok"
	}
	if reject := toolrejection.AsToolReject(err); reject != nil {
		return reject.Code
	}
	if refusal := toolrejection.HostRefusal(err); refusal != nil {
		return "host refusal"
	}
	return "error"
}

func profileAllowing(profiles []sandbox.ToolProfile, tool string) string {
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	for _, p := range profiles {
		if p.ID == "implement" && p.ToolAllowed(tool) {
			return p.ID
		}
	}
	for _, p := range profiles {
		if p.ToolAllowed(tool) {
			return p.ID
		}
	}
	return "implement"
}

// schemaSamples derives two calls from an argument schema: required
// properties only, and every property.
func schemaSamples(schema map[string]any) []map[string]any {
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	switch list := schema["required"].(type) {
	case []any:
		for _, item := range list {
			if s, ok := item.(string); ok {
				required[s] = true
			}
		}
	case []string:
		for _, s := range list {
			required[s] = true
		}
	}
	minimal, full := map[string]any{}, map[string]any{}
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		value := sampleValue(name, prop, 0)
		full[name] = value
		if required[name] {
			minimal[name] = cloneArgs(value)
		}
	}
	return []map[string]any{minimal, full}
}

func sampleValue(name string, prop map[string]any, depth int) any {
	if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	kind, _ := prop["type"].(string)
	if kinds, ok := prop["type"].([]any); ok && len(kinds) > 0 {
		kind, _ = kinds[0].(string)
	}
	switch kind {
	case "boolean":
		return true
	case "integer", "number":
		if minimum, ok := prop["minimum"].(float64); ok {
			return minimum
		}
		if minimum, ok := prop["minimum"].(int); ok {
			return float64(minimum)
		}
		return float64(1)
	case "array":
		items, _ := prop["items"].(map[string]any)
		if depth > 3 {
			return []any{}
		}
		return []any{sampleValue(name, items, depth+1)}
	case "object":
		out := map[string]any{}
		if depth > 3 {
			return out
		}
		nested, _ := prop["properties"].(map[string]any)
		for key, raw := range nested {
			child, _ := raw.(map[string]any)
			out[key] = sampleValue(key, child, depth+1)
		}
		return out
	default:
		return "a.log"
	}
}
