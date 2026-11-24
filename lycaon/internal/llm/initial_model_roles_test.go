package llm

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInitialRolesAreSavedIndependently(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	path := filepath.Join(t.TempDir(), "model-policy.yaml")
	store, err := NewPolicyStoreAt(path)
	testutil.FailErr(t, "open model policy", err)
	svc := Service{Policy: store}
	model := "gemma4-12b-paintedwolf"
	testutil.FailErr(t, "initialize roles", svc.initializeLocalRoles(model))
	read, err := NewPolicyStoreAt(path)
	testutil.FailErr(t, "reopen saved roles", err)
	policy := mustPolicyOverlay(t, read, SettingsScopeGlobal, "")
	expected := ModelRef{ProviderID: IncludedProviderID, Model: model}
	if policy.Coordinator != expected || policy.Lite != expected {
		t.Fatalf("initial roles were not saved: %+v", policy)
	}
	remote := ModelRef{ProviderID: "other", Model: "external-model"}
	patch := initialCoordinatorPatch(policy, ModelPolicyPatch{Coordinator: &remote})
	if patch.Lite != nil {
		t.Fatal("subsequent coordinator edit changed the summarizer")
	}
	testutil.FailErr(t, "change coordinator", store.PutGlobal(applyPolicyPatch(policy, patch)))
	testutil.FailErr(t, "switch local tier", svc.initializeLocalRoles("gemma4-26b-paintedwolf"))
	after := mustPolicyOverlay(t, store, SettingsScopeGlobal, "")
	if after.Coordinator != remote || after.Lite.Model != "gemma4-26b-paintedwolf" {
		t.Fatalf("tier switch: %+v", after)
	}
}

func TestInitialExternalCoordinatorSeedsOnlyUnsetSummarizer(t *testing.T) {
	coord := ModelRef{ProviderID: "other", Model: "coordinator"}
	lite := ModelRef{ProviderID: "other", Model: "summary"}
	initial := ModelPolicyPatch{Coordinator: &coord}
	patch := initialCoordinatorPatch(ModelPolicy{}, initial)
	if patch.Lite == nil || *patch.Lite != coord {
		t.Fatal("first coordinator did not initialize the summarizer")
	}
	if initialCoordinatorPatch(ModelPolicy{Lite: lite}, initial).Lite != nil {
		t.Fatal("lost saved summarizer")
	}
	initial.Lite = &lite
	if *initialCoordinatorPatch(ModelPolicy{}, initial).Lite != lite {
		t.Fatal("lost requested summarizer")
	}
}
