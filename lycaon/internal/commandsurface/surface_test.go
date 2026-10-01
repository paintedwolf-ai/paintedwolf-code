package commandsurface_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestIsCommandSurfaceError(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("pipeline stage 0: %w", argv.ErrShellMetacharacters),
		fmt.Errorf("pipeline stage 0: %w", argv.ErrCommandRequired),
		argv.ErrUnterminatedQuote,
		argv.ErrUnquotedNewline,
	} {
		if !commandsurface.IsCommandSurfaceError(err) {
			t.Fatalf("expected command-surface error: %v", err)
		}
	}
	if commandsurface.IsCommandSurfaceError(fmt.Errorf("unknown tool")) {
		t.Fatal("unexpected command-surface classification")
	}
	if commandsurface.IsCommandSurfaceError(fmt.Errorf("command contains shell metacharacters")) {
		t.Fatal("untyped prose must not classify")
	}
	if commandsurface.IsCommandSurfaceError(nil) {
		t.Fatal("nil must not classify")
	}
	for _, err := range []error{
		commandsurface.ErrArgvRequired,
		commandsurface.ErrArgvConflict,
		commandsurface.ErrPipelineShape,
	} {
		if commandsurface.IsCommandSurfaceError(err) {
			t.Fatalf("argv-shape error must not classify as command-surface: %v", err)
		}
	}
}
