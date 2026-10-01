package worker

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
)

func TestValidatePromoteSyntaxRejectsBrokenNewSource(t *testing.T) {
	issue := validatePromoteSyntax(context.Background(), []promoteMutation{{
		path: "new.py", targetExists: true, target: []byte("def broken(:\n    pass\n"),
	}})
	if issue == nil {
		t.Fatal("broken new source was promotable")
	}
	if issue.path != "new.py" {
		t.Fatalf("path = %q", issue.path)
	}
}

func TestValidatePromoteSyntaxAllowsUnsupportedAndCleanSource(t *testing.T) {
	plans := []promoteMutation{
		{path: "notes.txt", targetExists: true, target: []byte("free-form (( text")},
		{path: "main.go", targetExists: true, target: []byte("package main\n\nfunc main() {}\n")},
	}
	if issue := validatePromoteSyntax(context.Background(), plans); issue != nil {
		t.Fatalf("valid plans rejected: %+v", issue)
	}
}

func TestValidatePromoteSyntaxRequiresBrokenSourceToFinishClean(t *testing.T) {
	broken := []byte("package p\n\nfunc A( {\n\treturn 1\n}\n")
	issue := validatePromoteSyntax(context.Background(), []promoteMutation{{
		path: "a.go", originalExists: true, original: broken,
		targetExists: true, target: []byte("package p\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B( {\n"),
	}})
	if issue == nil {
		t.Fatal("partially repaired broken source was promotable")
	}
	if issue.change.Transition != syntaxhealth.TransitionFinalBroken {
		t.Fatalf("transition = %q want final_broken", issue.change.Transition)
	}
}

func TestPromotionCancellationReportsFailureAndSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	issue := validatePromoteSyntax(ctx, []promoteMutation{{path: "main.go", targetExists: true, target: []byte("package main\n")}})
	if issue == nil {
		t.Fatal("canceled promotion was accepted")
	}
	data := issue.rejectData("job")
	if data["parse_reason"] != "canceled" || data["parse_phase"] != "merged" || data["parse_source_bytes"] != len("package main\n") {
		t.Fatalf("promotion lost parser cause: %+v", data)
	}
	if _, ok := data["parse_error"]; ok {
		t.Fatalf("cancellation misreported as syntax error: %+v", data)
	}
}

func TestPromotionSyntaxOverrideIsExplicit(t *testing.T) {
	plans := []promoteMutation{{path: "a.go", targetExists: true, target: []byte("package p\nfunc (")}}
	ctx := syntaxhealth.WithOverride(context.Background(), "compiler-verified parser mismatch")
	if issue := validatePromoteSyntax(ctx, plans); issue != nil {
		t.Fatalf("explicit promotion override rejected: %+v", issue)
	}
	if issue := validatePromoteSyntax(context.Background(), plans); issue == nil {
		t.Fatal("override leaked into subsequent promotion")
	}
}
