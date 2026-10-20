package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
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
	if pathOperationError("fixture", denied) != denied {
		t.Fatal("adapter mapping replaced an authority refusal")
	}
	if pathOperationError("fixture", context.Canceled) != context.Canceled {
		t.Fatal("adapter mapping replaced cancellation")
	}
}
