package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Adapter-result classification is a contract fixture, not OS qualification.
func TestUnavailablePathAdapterPreservesTypedCause(t *testing.T) {
	source := fmt.Errorf("fixture adapter: %w", fseffect.ErrUnsupported)
	got := pathOperationError("fixture", source)
	var pathErr *PathError
	if !errors.As(got, &pathErr) || pathErr.Code != "COMMAND_PLATFORM_UNAVAILABLE" || !errors.Is(got, fseffect.ErrUnsupported) {
		t.Fatalf("unavailable path error lost classification: %v", got)
	}
	denied := &toolrejection.ToolReject{Code: "SURVEY_PATH_ESCAPE"}
	if !errors.Is(pathOperationError("fixture", denied), denied) {
		t.Fatal("adapter mapping replaced an authority refusal")
	}
	if !errors.Is(pathOperationError("fixture", context.Canceled), context.Canceled) {
		t.Fatal("adapter mapping replaced cancellation")
	}
}

// Every confinement failure of a held operand, including a profile scope
// denial, stops the program with the typed escape refusal and keeps its cause.
func TestConfinementFailuresBecomeTypedEscapeRefusals(t *testing.T) {
	scope := &sandbox.ScopeError{Path: "secret", Kind: sandbox.ScopeRead}
	for _, source := range []error{scope, fmt.Errorf("walk: %w", fseffect.ErrSymlink), fmt.Errorf("resolve: %w", sandbox.ErrPathEscape)} {
		got := pathOperationError("alias", source)
		reject := toolrejection.AsToolReject(got)
		if reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || reject.Data["path"] != "alias" || !errors.Is(got, source) {
			t.Fatalf("%v projected as %v", source, got)
		}
	}
}
