package policyindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPolicyUsesTheInvokedWorkerWorkspaceForEverySurface(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Project-tree policy")
	branch := t.TempDir()
	writePolicy(t, branch, "AGENTS.md", "Worker-branch policy")
	writePolicy(t, branch, "worker-only/AGENTS.md", "Worker nested policy")
	bound := append([]projectroot.RootRef(nil), roots.roots...)
	bound[0].Path = branch
	m.SetWorkspaceRoots(func(context.Context, *api.Session) ([]projectroot.RootRef, error) { return bound, nil })
	m.Warm(t.Context(), sess)
	index, ok := m.Index(t.Context(), sess)
	if !ok || !strings.Contains(index.Content, "worker-only/AGENTS.md") {
		t.Fatal("policy index did not follow the execution workspace")
	}
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "worker policy delivery", err)
	if len(delivery.Messages) != 1 || !strings.Contains(delivery.Messages[0].Content, "Worker-branch policy") || strings.Contains(delivery.Messages[0].Content, "Project-tree policy") {
		t.Fatal("worker policy delivery read another physical workspace")
	}
	check, err := bindPolicyRequest(m, t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "worker policy binding", err)
	testutil.FailErr(t, "worker root write", check(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: filepath.Join(branch, "new.go"), Mutation: true}}))
	policyReject(t, check(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: filepath.Join(branch, "worker-only/new.go"), Mutation: true}}), "POLICY_CONTEXT_CHANGED")
	chain, err := m.Chain(t.Context(), sess, "worker-only/new.go")
	testutil.FailErr(t, "worker path policy", err)
	if !strings.Contains(chain.Content, "Worker nested policy") {
		t.Fatal("path policy lookup did not follow worker workspace")
	}
	bound[0].Path = root.Path
	policyReject(t, check(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: filepath.Join(branch, "new.go"), Mutation: true}}), "POLICY_CONTEXT_UNAVAILABLE")
}
