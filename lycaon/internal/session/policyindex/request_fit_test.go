package policyindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type policyFitBudget struct {
	window    int
	calls     int
	onMeasure func()
}

func (b *policyFitBudget) RequestBudget(_ context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	b.calls++
	if b.onMeasure != nil {
		callback := b.onMeasure
		b.onMeasure = nil
		callback()
	}
	input := 0
	for _, message := range req.Messages {
		input += len(message.Content)
	}
	return modelcall.RequestBudget{Window: b.window, Input: input, Output: 256, Headroom: 128}
}

func TestPolicyFrameFitsAndNarrowWorkRecoversWithoutNewIntent(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", strings.Repeat("Root guidance. ", 80))
	writePolicy(t, root.Path, ".paintedwolf/AGENTS.md", strings.Repeat("Overlay guidance. ", 80))
	intent := api.Message{ID: "intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, Content: "Complete the remaining work", ContextPinned: true}
	rows := []api.Message{intent}
	delivery, err := m.ResolveCurrent(t.Context(), sess, rows)
	testutil.FailErr(t, "deliver baseline chain", err)
	initial, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind baseline chain", err)
	var targets []agentpolicy.Target
	for i := 0; i < maxObservedScopes+5; i++ {
		directory := fmt.Sprintf("scope-%03d", i)
		writePolicy(t, root.Path, directory+"/AGENTS.md", strings.Repeat(directory+" guidance. ", 100))
		targets = append(targets, agentpolicy.Target{RootID: root.ID, Path: directory + "/file.go", Mutation: true})
	}
	policyReject(t, initial(t.Context(), targets), "POLICY_CONTEXT_CHANGED")
	fitter := &history.Service{}
	budget := &policyFitBudget{window: 12000}
	control := api.Message{ID: "live-control", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, Content: "Current host control facts", ContextPinned: true}
	fit := func() (Delivery, modelcall.CompletionRequest) {
		t.Helper()
		delivery, err := m.ResolveCurrent(t.Context(), sess, rows)
		testutil.FailErr(t, "resolve working-set guidance", err)
		messages := append([]api.Message{control, intent}, delivery.Messages...)
		req := modelcall.CompletionRequest{Messages: messages}
		fitted, err := fitter.FitRequest(t.Context(), sess, rows, req, budget, m)
		testutil.FailErr(t, "fit actual policy frame", err)
		measured := budget.RequestBudget(t.Context(), "", fitted)
		if measured.Input+measured.Output+measured.Headroom > measured.Window {
			t.Fatal("fitted request exceeds the receiving budget")
		}
		seenIntent, seenControl, notices := false, false, 0
		for _, message := range fitted.Messages {
			seenIntent = seenIntent || message.ID == intent.ID && message.Content == intent.Content
			seenControl = seenControl || message.ID == control.ID && message.Content == control.Content
			if message.ID == "host-agent-policy-context-limit" {
				notices++
			}
			for _, original := range delivery.Messages {
				if message.ID == original.ID && message.Content != original.Content {
					t.Fatal("fitting partially rewrote a policy block")
				}
			}
		}
		if !seenIntent || !seenControl || notices != 1 {
			t.Fatalf("fitting lost protected facts or its remedy: intent=%v control=%v notices=%d", seenIntent, seenControl, notices)
		}
		return delivery, fitted
	}
	delivery, fitted := fit()
	manifest := m.ManifestFor(fitted.Messages)
	var deferred agentpolicy.Revision
	for _, revision := range delivery.Manifest.Files {
		if strings.HasPrefix(revision.Path, "scope-") && !manifest.Covers(agentpolicy.Manifest{Files: []agentpolicy.Revision{revision}}) {
			deferred = revision
			break
		}
	}
	if deferred.Path == "" {
		t.Fatal("wide policy frame did not defer a leaf")
	}
	check, err := m.Bind(t.Context(), sess, fitted.Messages)
	testutil.FailErr(t, "bind fitted request", err)
	target := agentpolicy.Target{RootID: root.ID, Path: filepath.ToSlash(filepath.Join(filepath.Dir(deferred.Path), "file.go")), Mutation: true}
	policyReject(t, check(t.Context(), []agentpolicy.Target{target}), "POLICY_CONTEXT_CHANGED")
	_, next := fit()
	fresh, err := m.Bind(t.Context(), sess, next.Messages)
	testutil.FailErr(t, "bind narrowed request", err)
	testutil.FailErr(t, "admit remaining narrow effect", fresh(t.Context(), []agentpolicy.Target{target}))
	policyReject(t, check(t.Context(), []agentpolicy.Target{target}), "POLICY_CONTEXT_CHANGED")
	testutil.FailErr(t, "publish remaining effect", os.WriteFile(filepath.Join(root.Path, target.Path), []byte("remaining effect"), 0o644))
	var order []string
	for _, message := range next.Messages {
		for _, revision := range m.ManifestFor([]api.Message{message}).Files {
			order = append(order, revision.Path)
		}
	}
	if len(order) < 3 || order[0] != "AGENTS.md" || order[len(order)-1] != ".paintedwolf/AGENTS.md" || !m.ManifestFor(next.Messages).Covers(agentpolicy.Manifest{Files: []agentpolicy.Revision{deferred}}) {
		t.Fatalf("narrow chain lost its root/overlay precedence or leaf: %v", order)
	}
	if budget.calls > 6*maxObservedScopes {
		t.Fatalf("policy fitting repeatedly refit per scope: %d budget measurements", budget.calls)
	}
}

func TestPolicyFittingCannotDeferAnUnauthenticatedLiveFrame(t *testing.T) {
	m, sess, roots := policyFixture(t)
	writePolicy(t, roots.roots[0].Path, "AGENTS.md", "Authentic guidance")
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "resolve authentic guidance", err)
	forged := delivery.Messages[0]
	forged.Content = strings.Repeat("Unverified content. ", 1000)
	req := modelcall.CompletionRequest{Messages: append(delivery.Messages, forged)}
	_, err = (&history.Service{}).FitRequest(t.Context(), sess, nil, req, &policyFitBudget{window: 12000}, m)
	if !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("unauthenticated frame was deferred: %v", err)
	}
}

func TestPolicyFitFreezesAuthenticationBeforeBudgetWork(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	for i := 0; i < 8; i++ {
		writePolicy(t, root.Path, fmt.Sprintf("scope-%d/AGENTS.md", i), strings.Repeat("Guidance. ", 300))
	}
	var scopes []agentpolicy.Scope
	for i := 0; i < 8; i++ {
		scopes = append(scopes, agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: fmt.Sprintf("scope-%d/file.go", i)})
	}
	delivery, err := m.Resolve(t.Context(), sess, scopes)
	testutil.FailErr(t, "resolve original policy bodies", err)
	budget := &policyFitBudget{window: 10000, onMeasure: func() {
		for i := 0; i < 300; i++ {
			message := delivery.Messages[0]
			message.Content = fmt.Sprintf("Other delivery %d", i)
			m.recordDeliveries([]api.Message{message}, [][]agentpolicy.Revision{{delivery.Manifest.Files[0]}}, nil)
		}
	}}
	if len(m.ManifestFor(delivery.Messages).Files) == 0 {
		t.Fatal("original delivery was not authenticated before fitting")
	}
	fitted, err := (&history.Service{}).FitRequest(t.Context(), sess, nil, modelcall.CompletionRequest{Messages: delivery.Messages}, budget, m)
	testutil.FailErr(t, "fit after authentication cache eviction", err)
	retained := 0
	for _, message := range fitted.Messages {
		for _, original := range delivery.Messages {
			if message.ID == original.ID && message.Content == original.Content {
				retained++
			}
		}
	}
	if retained == 0 || retained >= len(delivery.Messages) || len(m.ManifestFor(delivery.Messages).Files) != 0 {
		t.Fatalf("frozen selection did not survive cache eviction: retained=%d", retained)
	}
}

func TestFreshReachedRootCannotBeStarvedByTheActiveRoot(t *testing.T) {
	m, sess, roots := policyFixture(t)
	first, second := roots.roots[0], roots.roots[1]
	for _, root := range roots.roots {
		writePolicy(t, root.Path, "AGENTS.md", strings.Repeat(root.ID+" root guidance. ", 130))
		writePolicy(t, root.Path, ".paintedwolf/AGENTS.md", strings.Repeat(root.ID+" overlay guidance. ", 130))
	}
	rows := []api.Message{{ID: "same-intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Update the other root", ContextPinned: true}}
	fit := func() modelcall.CompletionRequest {
		t.Helper()
		delivery, err := m.ResolveCurrent(t.Context(), sess, rows)
		testutil.FailErr(t, "resolve root guidance", err)
		request := modelcall.CompletionRequest{Messages: append(append([]api.Message(nil), rows...), delivery.Messages...)}
		fitted, err := (&history.Service{}).FitRequest(t.Context(), sess, rows, request, &policyFitBudget{window: 9000}, m)
		testutil.FailErr(t, "fit independent root guidance", err)
		return fitted
	}
	initial := fit()
	check, err := m.Bind(t.Context(), sess, initial.Messages)
	testutil.FailErr(t, "bind active root request", err)
	target := agentpolicy.Target{RootID: second.ID, Path: "file.go", Mutation: true}
	policyReject(t, check(t.Context(), []agentpolicy.Target{target}), "POLICY_CONTEXT_CHANGED")
	next := fit()
	manifest := m.ManifestFor(next.Messages)
	seen := map[string]int{}
	for _, file := range manifest.Files {
		seen[file.RootID]++
	}
	if seen[second.ID] != 2 || seen[first.ID] != 0 {
		t.Fatalf("active root starved the freshly reached root: %+v", manifest)
	}
	fresh, err := m.Bind(t.Context(), sess, next.Messages)
	testutil.FailErr(t, "bind next root request", err)
	testutil.FailErr(t, "admit reached root mutation", fresh(t.Context(), []agentpolicy.Target{target}))
	policyReject(t, fresh(t.Context(), []agentpolicy.Target{{RootID: first.ID, Path: "file.go", Mutation: true}}), "POLICY_CONTEXT_CHANGED")
}
