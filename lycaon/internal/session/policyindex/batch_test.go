package policyindex

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoundPolicyResolvesOneSnapshotForManyOperands(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Shared policy")
	resolutions := 0
	m.SetWorkspaceRoots(func(context.Context, *api.Session) ([]projectroot.RootRef, error) {
		resolutions++
		return roots.roots, nil
	})
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "shared policy delivery", err)
	check, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind shared policy", err)
	var targets []agentpolicy.Target
	for i := 0; i < 1000; i++ {
		targets = append(targets, agentpolicy.Target{RootID: root.ID, Path: fmt.Sprintf("file-%d.go", i), Mutation: true})
	}
	resolutions = 0
	testutil.FailErr(t, "many-operand effect", check(t.Context(), targets))
	if resolutions != 1 {
		t.Fatalf("one operation used %d policy workspace snapshots", resolutions)
	}
	state, _ := m.observed.Load(sess.ID)
	if len(state.scopes) != 1 || state.limit != nil {
		t.Fatal("many operands sharing policy expanded the retained working set")
	}
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "repeat shared policy delivery", err)
	fresh, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind repeat request", err)
	testutil.FailErr(t, "repeat many-operand effect", fresh(t.Context(), targets))
}

func TestPolicyScopeLimitIsExplicitAndNarrowWorkRecovers(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "root policy delivery", err)
	check, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind root policy", err)
	var targets []agentpolicy.Target
	for i := 0; i < maxObservedScopes+5; i++ {
		directory := fmt.Sprintf("scope-%d", i)
		writePolicy(t, root.Path, directory+"/AGENTS.md", "Nested policy")
		targets = append(targets, agentpolicy.Target{RootID: root.ID, Path: directory + "/file.go", Mutation: true})
	}
	err = check(t.Context(), targets)
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != "POLICY_CONTEXT_CHANGED" || rejection.Data["reason"] != "scope_limit" {
		t.Fatalf("large policy working set did not explain its limit: %v", err)
	}
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "limited policy delivery", err)
	if delivery.Status != StatusScopeLimit {
		t.Fatal("scope limit was not carried into the next model request")
	}
	state, _ := m.observed.Load(sess.ID)
	if len(state.scopes) != maxObservedScopes || state.limit == nil {
		t.Fatal("limited policy working set was not bounded")
	}
	testutil.FailErr(t, "narrow effect after limit", check(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: "file.go", Mutation: true}}))
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "recovered policy delivery", err)
	if delivery.Status == StatusScopeLimit {
		t.Fatal("a completed narrow operation retained the older batch's limit")
	}
}
