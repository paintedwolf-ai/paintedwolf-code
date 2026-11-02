package llm

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRuntimePolicyDefaultsReachWorkersWithoutPersistence(t *testing.T) {
	store := NewInMemoryPolicyStore(ModelPolicy{})
	available := true
	router := NewStaticModelRouter(store).WithLocalDefault(func() bool { return available })
	for _, view := range []*StaticModelRouter{router, router.WithScope(SettingsScopeProject, t.TempDir())} {
		for _, ready := range []bool{true, false, true} {
			available = ready
			want := ModelRef{}
			if ready {
				want = DefaultLocalModelRef()
			}
			selected, err := view.Select(t.Context())
			testutil.FailErr(t, "select worker", err)
			if selected.ProviderID != want.ProviderID || selected.Model != want.Model {
				t.Fatalf("worker = %+v, want %+v", selected, want)
			}
			group, err := view.SelectGroup(t.Context(), 3)
			testutil.FailErr(t, "select worker group", err)
			for _, member := range group {
				if member.ProviderID != want.ProviderID || member.Model != want.Model {
					t.Fatalf("group member = %+v, want %+v", member, want)
				}
			}
			child, err := view.ResolveSession(t.Context(), &api.Session{ParentSessionID: "parent"})
			testutil.FailErr(t, "resolve worker session", err)
			if child.ProviderID != want.ProviderID || child.Model != want.Model {
				t.Fatalf("child = %+v, want %+v", child, want)
			}
		}
	}
	stored, err := store.Overlay(SettingsScopeGlobal, "")
	testutil.FailErr(t, "read stored assignments", err)
	if modelRefSet(stored.Coordinator) || modelRefSet(stored.Lite) || len(stored.AgentPool.Models) != 0 {
		t.Fatalf("defaults persisted: %+v", stored)
	}
}

func TestResolvedPolicyPreservesExplicitAssignmentsAndOwnsItsSlices(t *testing.T) {
	remote := ModelRef{ProviderID: "remote", Model: "chosen"}
	original := ModelPolicy{Coordinator: remote, Lite: remote, AgentPool: AgentPool{Models: []ModelRef{remote}}}
	resolved := ResolveModelPolicy(original, true)
	if !reflect.DeepEqual(resolved, original) {
		t.Fatalf("explicit policy changed: %+v", resolved)
	}
	resolved.AgentPool.Models[0].Model = "changed"
	if original.AgentPool.Models[0] != remote {
		t.Fatal("resolved policy aliases stored pool")
	}
}
