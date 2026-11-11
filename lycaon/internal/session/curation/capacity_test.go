package curation

import (
	"context"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type capacityRoots struct{}

func (capacityRoots) SettingsRoots(context.Context, *wire.Session) []string { return nil }

func TestCurationSharesAliasedRuntime(t *testing.T) {
	runtime := nativegen.NewRuntime(nativegen.RuntimeConfig{})
	defer runtime.Close()
	independent := nativegen.NewRuntime(nativegen.RuntimeConfig{})
	defer independent.Close()
	registry := &llm.Registry{}
	for id, owner := range map[string]*nativegen.Runtime{"coordinator": runtime, "utility": runtime, "independent": independent} {
		testutil.FailErr(t, "register native provider", registry.Register(nativegen.New(id, nativegen.Options{Runtime: owner})))
	}
	service := New(nil, capacityRoots{}, nil, nil, nil)
	service.SetModelSources(llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "coordinator", Model: "gemma4:e4b"},
		Lite:        llm.ModelRef{ProviderID: "utility", Model: "gemma4:e4b"},
	}), registry)
	for _, tc := range []struct {
		id     string
		shared bool
	}{
		{"", true}, {"coordinator", true}, {"utility", true}, {"independent", false}, {"missing", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			sess := &wire.Session{ProviderID: tc.id}
			if got := service.SharesCoordinatorCapacity(t.Context(), sess); got != tc.shared {
				t.Fatalf("shared = %v, want %v", got, tc.shared)
			}
			if tc.shared {
				// Deferred curation does not require admission until the turn releases it.
				if service.Begin(t.Context(), sess, "Name this session") == nil {
					t.Fatal("missing deferred curation")
				}
			}
		})
	}
}
