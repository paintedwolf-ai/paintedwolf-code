package llm

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestThinkingPolicyLayersAndRemoval(t *testing.T) {
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy", err)
	global := modelcall.ThinkingOverride{ProviderID: "one", Model: "shared", Mode: "fixed", Effort: "high"}
	other := modelcall.ThinkingOverride{ProviderID: "two", Model: "shared", Mode: "fixed", Effort: "low"}
	testutil.FailErr(t, "save device", store.PutGlobal(ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{global, other}}))
	primary, active := t.TempDir(), t.TempDir()
	project := global
	project.Mode, project.Effort = "application", ""
	testutil.FailErr(t, "save thinking-only project", store.PutProject(primary, ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{project}}))
	got, err := store.GetForProjectRoots([]string{primary, active})
	testutil.FailErr(t, "resolve roots", err)
	if !reflect.DeepEqual(got.ThinkingOverrides, []modelcall.ThinkingOverride{project, other}) {
		t.Fatalf("wrong inherited overrides: %+v", got.ThinkingOverrides)
	}
	activeOverride := global
	activeOverride.Effort = "max"
	testutil.FailErr(t, "save active project", store.PutProject(active, ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{activeOverride}}))
	got, err = store.GetForProjectRoots([]string{primary, active})
	testutil.FailErr(t, "resolve active", err)
	if got.ThinkingOverrides[0].Effort != "max" {
		t.Fatalf("active override lost: %+v", got)
	}
	empty := []modelcall.ThinkingOverride{}
	current, err := store.Overlay(SettingsScopeProject, active)
	testutil.FailErr(t, "read active overlay", err)
	testutil.FailErr(t, "remove active override", store.PutProject(active, applyPolicyPatch(current, ModelPolicyPatch{ThinkingOverrides: &empty})))
	got = mustPolicy(t, store, SettingsScopeProject, active)
	if got.ThinkingOverrides[0].Effort != "high" {
		t.Fatalf("removal did not inherit device: %+v", got)
	}
	reopened, err := NewPolicyStoreAt(store.globalPath)
	testutil.FailErr(t, "reopen device policy", err)
	if !reflect.DeepEqual(mustPolicy(t, reopened, SettingsScopeGlobal, "").ThinkingOverrides, []modelcall.ThinkingOverride{global, other}) {
		t.Fatal("thinking policy did not survive disk round trip")
	}
}

func TestThinkingPolicyOwnsMutableValues(t *testing.T) {
	on := true
	p := ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "p", Model: "m", Mode: "fixed", Enabled: &on}}}
	store := NewInMemoryPolicyStore(p)
	ctx := WithThinkingPolicy(t.Context(), p)
	on = false
	p.ThinkingOverrides[0].Mode = "application"
	first := mustPolicy(t, store, SettingsScopeGlobal, "")
	if !*first.ThinkingOverrides[0].Enabled {
		t.Fatal("caller mutated store")
	}
	*first.ThinkingOverrides[0].Enabled = false
	if !*mustPolicy(t, store, SettingsScopeGlobal, "").ThinkingOverrides[0].Enabled {
		t.Fatal("reader mutated store")
	}
	frozen, ok := thinkingPolicyFromContext(ctx)
	if !ok || frozen[0].Mode != "fixed" || !*frozen[0].Enabled {
		t.Fatal("turn snapshot changed with caller")
	}
}

func TestThinkingPolicyRejectsAmbiguousYAML(t *testing.T) {
	for _, fields := range []string{
		"mode: fixed",
		"mode: automatic",
		"mode: application, effort: high",
		"mode: fixed, effort: high, enabled: false",
		"mode: fixed, budget_tokens: 0",
		"mode: fixed, effort: HIGH",
		"mode: fixed, budget_tokens: 9223372036854775807",
	} {
		if _, err := decodePolicy([]byte("thinking_overrides: [{provider_id: p, model: m, "+fields+"}]"), "fixture"); err == nil {
			t.Fatalf("accepted ambiguous override: %s", fields)
		}
	}
	o := modelcall.ThinkingOverride{ProviderID: "p", Model: "m", Mode: "application"}
	if err := validateThinkingOverrides([]modelcall.ThinkingOverride{o, o}); err == nil {
		t.Fatal("accepted duplicate provider/model policy")
	}
}

func TestThinkingAssignmentPatchPreservesOverrides(t *testing.T) {
	original := ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "p", Model: "m", Mode: "fixed", Effort: "low"}}}
	ref := ModelRef{ProviderID: "p", Model: "new"}
	next := applyPolicyPatch(original, ModelPolicyPatch{Coordinator: &ref})
	if !reflect.DeepEqual(next.ThinkingOverrides, original.ThinkingOverrides) {
		t.Fatal("assignment patch erased override")
	}
	empty := []modelcall.ThinkingOverride{}
	next = applyPolicyPatch(next, ModelPolicyPatch{ThinkingOverrides: &empty})
	if len(next.ThinkingOverrides) != 0 || next.Coordinator != ref {
		t.Fatal("thinking reset changed model assignment")
	}
}

func TestThinkingPolicyCanRemoveStaleEntriesIndividually(t *testing.T) {
	first := modelcall.ThinkingOverride{ProviderID: "gone-one", Model: "m", Mode: "fixed", Effort: "high"}
	second := modelcall.ThinkingOverride{ProviderID: "gone-two", Model: "m", Mode: "fixed", Effort: "high"}
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create editable policy", err)
	testutil.FailErr(t, "save stale overrides", store.PutGlobal(ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{first, second}}))
	service := &Service{Policy: store}
	remaining := []modelcall.ThinkingOverride{second}
	testutil.FailErr(t, "remove one unavailable override", service.ApplyModelPolicy(t.Context(), SettingsScopeGlobal, "", ModelPolicyPatch{ThinkingOverrides: &remaining}))
	if got := mustPolicy(t, store, SettingsScopeGlobal, "").ThinkingOverrides; !reflect.DeepEqual(got, remaining) {
		t.Fatalf("stale override removal changed remaining settings: %+v", got)
	}
	remaining[0].Effort = "low"
	if err := service.ApplyModelPolicy(t.Context(), SettingsScopeGlobal, "", ModelPolicyPatch{ThinkingOverrides: &remaining}); err == nil {
		t.Fatal("accepted a changed override without provider capability validation")
	}
}
