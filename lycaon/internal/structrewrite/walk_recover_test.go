package structrewrite

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestSourceAnalysisRecoveredRetainsDiagnostics(t *testing.T) {
	for _, operation := range []string{"structural match", "extract symbol", "walk search", "file search for panic.go"} {
		t.Run(operation, func(t *testing.T) {
			failure := sourceAnalysisRecovered(operation, "swift", 123, "forced panic")
			if failure.Reason != "panic" || failure.Language != "swift" || failure.SourceBytes != 123 {
				t.Fatalf("panic lost its diagnostics: %+v", failure)
			}
			if failure.Cause == nil || !strings.Contains(failure.Detail, operation) || !strings.Contains(failure.Detail, "forced panic") {
				t.Fatalf("panic lost its operation or cause: %+v", failure)
			}
		})
	}
}

func TestWalkSearchPanicReturnsFailure(t *testing.T) {
	dir := t.TempDir()
	writeWalkFile(t, dir, "main.go", "package main\nfunc main() { f(1) }\n")
	files, truncated, err := WalkSearch(t.Context(), WalkRequest{
		Root: dir, Pattern: "f($A)",
		PathIncluded: func(string, bool) bool { panic("source boundary failed") },
	})
	var failure *tsparse.Failure
	if !errors.As(err, &failure) || failure.Reason != "panic" {
		t.Fatalf("walk panic lost its parser failure: %v", err)
	}
	if len(files) != 0 || truncated || !strings.Contains(failure.Detail, "source boundary failed") {
		t.Fatalf("walk panic claimed results or lost its cause: files=%+v truncated=%v failure=%+v", files, truncated, failure)
	}
}
