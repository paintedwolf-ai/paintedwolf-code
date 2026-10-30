package policyindex

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func policyReject(t *testing.T, err error, code string) {
	t.Helper()
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != code {
		t.Fatalf("policy rejection = %v, want %s", err, code)
	}
}

func TestBoundPolicyRequiresTheProposingRequestToCoverReachedPolicy(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	writePolicy(t, root.Path, "backend/AGENTS.md", "Backend policy")
	history := []api.Message{{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Implement"}}
	delivery, err := m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "baseline policy", err)
	old, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind baseline policy", err)
	path := filepath.Join(root.Path, "backend/new.go")
	policyReject(t, old(t.Context(), root.ID, path, true), "POLICY_CONTEXT_CHANGED")
	next, err := m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "reached policy delivery", err)
	if len(next.Manifest.Files) != 2 {
		t.Fatal("held operation's scope was not delivered on the next request")
	}
	fresh, err := m.Bind(t.Context(), sess, next.Messages)
	testutil.FailErr(t, "bind reached policy", err)
	testutil.FailErr(t, "fresh request mutation", fresh(t.Context(), root.ID, path, true))
	policyReject(t, old(t.Context(), root.ID, path, true), "POLICY_CONTEXT_CHANGED")
	writePolicy(t, root.Path, "backend/AGENTS.md", "Changed backend policy")
	policyReject(t, fresh(t.Context(), root.ID, path, true), "POLICY_CONTEXT_CHANGED")
}

func TestBoundPolicyChecksBothRootsOfAPathTransfer(t *testing.T) {
	m, sess, roots := policyFixture(t)
	first, second := roots.roots[0], roots.roots[1]
	writePolicy(t, first.Path, "AGENTS.md", "Source policy")
	writePolicy(t, second.Path, "AGENTS.md", "Destination policy")
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "source policy", err)
	check, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind source policy", err)
	testutil.FailErr(t, "source path admission", check(t.Context(), first.ID, "from.go", true))
	policyReject(t, check(t.Context(), second.ID, "to.go", true), "POLICY_CONTEXT_CHANGED")
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "transfer policy", err)
	check, err = m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind transfer policy", err)
	testutil.FailErr(t, "source path admission", check(t.Context(), first.ID, "from.go", true))
	testutil.FailErr(t, "destination path admission", check(t.Context(), second.ID, "to.go", true))
}

func TestBoundPolicyReadsDiscoverScopeWithoutHoldingInvestigation(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	writePolicy(t, root.Path, "nested/AGENTS.md", "Nested policy")
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "baseline policy", err)
	check, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind baseline policy", err)
	testutil.FailErr(t, "investigative read", check(t.Context(), root.ID, "nested/file.go", false))
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "read scope policy", err)
	if len(delivery.Manifest.Files) != 2 {
		t.Fatal("successful read scope was not included in the next request")
	}
}

func TestBoundPolicyTrustAndCancellationRetainTheirMeaning(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	roots.disabled = true
	check, err := m.Bind(t.Context(), sess, nil)
	testutil.FailErr(t, "bind disabled surface", err)
	testutil.FailErr(t, "disabled policy admission", check(t.Context(), root.ID, "file.go", true))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := check(ctx, root.ID, "file.go", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became policy recovery: %v", err)
	}
}
