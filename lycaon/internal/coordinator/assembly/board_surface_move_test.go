package assembly

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// orientOnceBoard renders until it is oriented, then reports no change until
// its orientation is invalidated, as the board engine does.
type orientOnceBoard struct {
	oriented bool
	renders  int
}

func (b *orientOnceBoard) PrependBoardIfChanged(context.Context, *api.Session, api.CoordinatorRunContext) (string, bool) {
	if b.oriented {
		return "", false
	}
	b.oriented = true
	b.renders++
	return fmt.Sprintf("board render %d", b.renders), true
}
func (b *orientOnceBoard) WorkerBoard(context.Context, *api.Session) (string, bool) { return "", false }
func (b *orientOnceBoard) BoardInjectHash(string) string                            { return "" }
func (b *orientOnceBoard) InvalidateOrientation(string)                             { b.oriented = false }

type countingOrientRecorder struct{ records int }

func (r *countingOrientRecorder) RecordBoardOrientReady(context.Context, string, string) error {
	r.records++
	return nil
}

// A later iteration reuses the turn's board; a turn that moved surface renders
// it again for the surface it is on.
func TestPrepareCoordinatorBoardRendersAgainForAMovedTurn(t *testing.T) {
	board := &orientOnceBoard{}
	oriented := &countingOrientRecorder{}
	e := &turnContextAssembler{deps: turnContextDeps{Board: board, BoardOrientReady: oriented}}
	sess := &api.Session{ID: "moved-board"}
	turn := &TurnAssemblyScratch{SurfaceID: "implement_investigate"}
	prepare := func() string {
		t.Helper()
		_, block, err := e.prepareCoordinatorBoard(context.Background(), sess, inject.CoordinatorTurnFrame{}, turn)
		testutil.FailErr(t, "prepareCoordinatorBoard", err)
		return block
	}
	first := prepare()
	turn.Iteration = 1
	if again := prepare(); again != first || board.renders != 1 {
		t.Fatalf("same surface: block %q after %d renders, want %q after 1", again, board.renders, first)
	}
	turn.SurfaceID = surface.SurfaceImplementPark
	if moved := prepare(); moved == first || board.renders != 2 || turn.BoardSurfaceID != surface.SurfaceImplementPark {
		t.Fatalf("moved surface: block %q after %d renders for %q, want a second render for park", moved, board.renders, turn.BoardSurfaceID)
	}
	if oriented.records != board.renders {
		t.Fatalf("%d orientations recorded for %d renders", oriented.records, board.renders)
	}
}
