package settings

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestExactPathGrantCoversAllDeclaredTargets(t *testing.T) {
	for _, path := range []string{"/project/file.txt", "/project/file*.txt", "/project/file?.txt", "/project/file[ab].txt"} {
		t.Run(path, func(t *testing.T) {
			action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{path},
},
Scope: hitl.ActionScope{
ProjectID: "project",
},
}
			predicate := GrantPredicateForAction(action)
			grant := hitl.ApprovalGrant{ProjectID: action.Scope.ProjectID,
				Predicate: hitl.ApprovalGrantPredicate{Category: string(predicate.Category), Pattern: predicate.Pattern}}
			for _, tc := range []struct {
				name    string
				files   []string
				covered bool
			}{
				{"approved target", []string{path}, true},
				{"duplicate approved target", []string{path, path}, true},
				{"no declared targets", nil, false},
				{"other target", []string{"/project/filea.txt"}, false},
				{"mixed targets", []string{path, "/project/other.txt"}, false},
				{"same basename elsewhere", []string{"/other/file.txt"}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					action.Invocation.Files = tc.files
					if covered := grantMatchesAction(grant, action); covered != tc.covered {
						t.Fatalf("grant for %q with targets %v: covered=%t, want %t", path, tc.files, covered, tc.covered)
					}
				})
			}
		})
	}
}

func TestToolGrantsDoNotInterpretPatternSyntax(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
},
Scope: hitl.ActionScope{
ProjectID: "project",
},
}
	for _, pattern := range []string{"*", "r*", "r?ad", "r[ea]ad", ""} {
		grant := hitl.ApprovalGrant{ProjectID: action.Scope.ProjectID,
			Predicate: hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryTool), Pattern: pattern}}
		if grantMatchesAction(grant, action) {
			t.Fatalf("literal tool grant %q covered read", pattern)
		}
	}
}

func TestRelativePathGrantRetainsItsOriginatingRoot(t *testing.T) {
	grant := hitl.ApprovalGrant{ProjectID: "project", ProjectDir: "/project/primary",
		Predicate: hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryPath), Pattern: "file.txt"}}
	for _, tc := range []struct {
		name    string
		root    string
		path    string
		covered bool
	}{
		{"same relative target", "/project/primary", "file.txt", true},
		{"absolute equivalent", "/project/primary", "/project/primary/file.txt", true},
		{"other root same relative path", "/project/secondary", "file.txt", false},
		{"same absolute target from other root", "/project/secondary", "/project/primary/file.txt", true},
		{"other root absolute target", "/project/primary", "/project/secondary/file.txt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{tc.path},
},
Scope: hitl.ActionScope{
ProjectID: grant.ProjectID,
ProjectDir: tc.root,
},
}
			if covered := grantMatchesAction(grant, action); covered != tc.covered {
				t.Fatalf("covered=%t, want %t for %s in %s", covered, tc.covered, tc.path, tc.root)
			}
		})
	}
}

func TestPathGrantsUsePhysicalInvocationTargets(t *testing.T) {
	const target = "/branches/job/root-a/file[ab].txt"
	grant := hitl.ApprovalGrant{ProjectID: "project", ProjectDir: "/project/a",
		Predicate: hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryPath), Pattern: target}}
	for _, tc := range []struct {
		name               string
		declared, resolved []string
		covered            bool
	}{
		{"branch read", []string{"file[ab].txt"}, []string{target}, true},
		{"qualified read", []string{"@a/file[ab].txt"}, []string{target}, true},
		{"absolute equivalent", []string{target}, []string{target}, true},
		{"duplicate target", []string{"file[ab].txt", target}, []string{target, target}, true},
		{"other root", []string{"file[ab].txt"}, []string{"/branches/job/root-b/file[ab].txt"}, false},
		{"other branch", []string{"file[ab].txt"}, []string{"/branches/other/root-a/file[ab].txt"}, false},
		{"literal neighbor", []string{"filea.txt"}, []string{"/branches/job/root-a/filea.txt"}, false},
		{"mixed targets", []string{"file[ab].txt", "other.txt"}, []string{target, "/branches/job/root-a/other.txt"}, false},
		{"partial resolution", []string{"file[ab].txt", "@missing/file.txt"}, []string{target, ""}, false},
		{"missing resolution", []string{"file[ab].txt"}, []string{""}, false},
		{"relative resolution", []string{"file[ab].txt"}, []string{"file[ab].txt"}, false},
		{"empty resolution", []string{"file[ab].txt"}, []string{}, false},
		{"missing target", []string{"file[ab].txt", "other.txt"}, []string{target}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: tc.declared,
ResolvedFiles: tc.resolved,
},
Scope: hitl.ActionScope{
ProjectID: grant.ProjectID,
ProjectDir: grant.ProjectDir,
},
}
			if got := grantMatchesAction(grant, action); got != tc.covered {
				t.Fatalf("physical target coverage=%t, want %t", got, tc.covered)
			}
			action.Invocation.Tool = "write"
			predicate := GrantPredicateForAction(action)
			want := ""
			if tc.covered && len(tc.declared) == 1 {
				want = target
			}
			if !tc.covered && len(tc.declared) == 1 && len(tc.resolved) == 1 && len(tc.resolved[0]) > 0 && tc.resolved[0][0] == '/' {
				want = tc.resolved[0]
			}
			if predicate.Pattern != want {
				t.Fatalf("minted path=%q, want %q", predicate.Pattern, want)
			}
		})
	}
}

func TestWorkerWriteGrantCoversReadAtTheSamePhysicalTarget(t *testing.T) {
	approvals := ladderGate(t)
	testutil.FailErr(t, "configure path approval", approvals.store.PutGlobal(ApprovalConfig{Rules: []ApprovalRule{
		{Category: ApprovalCategoryPath, Pattern: "**", Effect: ApprovalEffectAsk},
	}}))
	branch, project := t.TempDir(), t.TempDir()
	path := filepath.Join(branch, "literal[ab].txt")
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{path},
ResolvedFiles: []string{path},
},
Scope: hitl.ActionScope{
ProjectID: "project",
ProjectDir: project,
SessionID: "worker",
RootSessionID: "chat",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{branch}},
},
}
	first, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate prepared write", err)
	if !first.Required() {
		t.Fatal("unapproved write did not ask")
	}
	var installed bool
	for _, offer := range approvals.GrantOffers(action, first) {
		if offer.Scope == hitl.ApprovalGrantScopeChat && offer.Grant.Predicate.Category == string(ApprovalCategoryPath) {
			installed, err = approvals.ApplyGrant(offer.Grant)
			testutil.FailErr(t, "install path grant", err)
			break
		}
	}
	if !installed {
		t.Fatal("task path grant was not installed")
	}
	action.Invocation.Tool = "read"
	action.Invocation.Files = []string{"literal[ab].txt"}
	action.Invocation.Args = map[string]any{"path": "literal[ab].txt"}
	repeat, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate worker read", err)
	if !repeat.AutoApproved() {
		t.Fatalf("same physical target asked again: %+v", repeat)
	}
	action.Invocation.ResolvedFiles = []string{filepath.Join(project, "literal[ab].txt")}
	other, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate other physical target", err)
	if !other.Required() {
		t.Fatalf("same basename in source project inherited branch grant: %+v", other)
	}
}
