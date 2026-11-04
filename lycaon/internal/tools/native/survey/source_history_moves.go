package survey

import (
	"context"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// maxMoveHops bounds how far a chain of recorded moves is followed.
const maxMoveHops = 8

// ledgerLocation is a file's place in the ledger.
type ledgerLocation struct{ rootID, rel string }

// recordedMoveSource follows the moves a branch recorded back from a location,
// through the file itself or any directory holding it, to where it began.
func recordedMoveSource(ctx context.Context, tctx tools.ToolContext, files tools.SourceFileHistory, branch sourcebranch.ID, at ledgerLocation) (ledgerLocation, bool, error) {
	moved := false
	for range maxMoveHops {
		source, found, err := moveInto(ctx, tctx, files, branch, at)
		if err != nil {
			return ledgerLocation{}, false, err
		}
		if !found {
			break
		}
		at, moved = source, true
	}
	return at, moved, nil
}

func moveInto(ctx context.Context, tctx tools.ToolContext, files tools.SourceFileHistory, branch sourcebranch.ID, at ledgerLocation) (ledgerLocation, bool, error) {
	for held := at.rel; ; held = path.Dir(held) {
		effect, found, err := files.OldestPathEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, branch, at.rootID, held, 0)
		if err != nil {
			return ledgerLocation{}, false, err
		}
		if found && effect.FromPath != "" && (effect.FromRootID != at.rootID || effect.FromPath != held) {
			return ledgerLocation{rootID: effect.FromRootID, rel: effect.FromPath + strings.TrimPrefix(at.rel, held)}, true, nil
		}
		if !strings.Contains(held, "/") {
			return ledgerLocation{}, false, nil
		}
	}
}

// recordedMoveAway reports whether the branch recorded moving this location, or a
// directory holding it, somewhere else. It is asked of a path with no head, so an
// effect at the path itself means its file has since left.
func recordedMoveAway(ctx context.Context, tctx tools.ToolContext, files tools.SourceFileHistory, branch sourcebranch.ID, at ledgerLocation) (bool, error) {
	for held := at.rel; ; held = path.Dir(held) {
		effect, found, err := files.OldestPathEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, branch, at.rootID, held, 0)
		if err != nil {
			return false, err
		}
		if found && (held == at.rel || (effect.FromRootID == at.rootID && effect.FromPath == held)) {
			return true, nil
		}
		if !strings.Contains(held, "/") {
			return false, nil
		}
	}
}

// movedBase gives a path the baseline never held the text of what moved there. The
// baseline at the move's source comes first, since a program may have changed the
// file before the move and the ledger captured it after. A source the baseline lacks
// leaves absence: the file was created this leg. Where the baseline holds no text,
// the ledger's predecessor answers. beforeAt names where the text came from.
func movedBase(
	ctx context.Context,
	tctx tools.ToolContext,
	ledger tools.SourceHistory,
	branch sourcebranch.ID,
	at ledgerLocation,
	recorded ledgerDiff,
	compared *workspacebaseline.PathComparison,
	compareSource func(path string) (workspacebaseline.PathComparison, error),
) (beforeAt string, moved bool, err error) {
	var predecessor *sourceledger.ComparisonSide
	var source ledgerLocation
	if comp := recorded.comp; comp != nil && comp.Before.VersionID != "" && (comp.Before.RootID != at.rootID || comp.Before.Path != at.rel) {
		predecessor, source, moved = &comp.Before, ledgerLocation{rootID: comp.Before.RootID, rel: comp.Before.Path}, true
	} else if source, moved, err = recordedMoveSource(ctx, tctx, ledger.Files, branch, at); err != nil || !moved {
		return "", false, err
	}
	beforeAt = projectroot.DisplayPath(tctx.Source.Roots, source.rootID, source.rel)
	baseline, err := compareSource(beforeAt)
	if err != nil {
		return "", false, err
	}
	switch {
	case baseline.Absent || baseline.Kind == workspacebaseline.ChangeCreated:
		return "", true, nil
	case baseline.Kind != "" && baseline.HasBefore:
		compared.Before, compared.HasBefore = baseline.Before, true
	case predecessor != nil:
		compared.Before, compared.HasBefore = predecessor.Content, sideReadable(*predecessor)
	default:
		compared.HasBefore = false
	}
	return beforeAt, true, nil
}

// displayMove is a recorded move in display paths.
type displayMove struct{ from, to string }

// recordedMoves lists a page's moves oldest first. Effects arrive newest first.
func recordedMoves(roots []projectroot.RootRef, effects []sourceledger.Effect) []displayMove {
	var moves []displayMove
	for i := len(effects) - 1; i >= 0; i-- {
		if effect := effects[i]; effect.FromPath != "" {
			moves = append(moves, displayMove{
				from: projectroot.DisplayPath(roots, effect.FromRootID, effect.FromPath),
				to:   projectroot.DisplayPath(roots, effect.RootID, effect.Path),
			})
		}
	}
	return moves
}

func throughMoves(moves []displayMove, at string) string {
	for _, move := range moves {
		if at == move.from {
			at = move.to
		} else if rest, ok := strings.CutPrefix(at, move.from+"/"); ok {
			at = move.to + "/" + rest
		}
	}
	return at
}

// coveredByMoves returns the delete and create rows a recorded move explains: their
// paths map through it and the bytes are the baseline's. Anything else keeps its
// row, so a generated file is never hidden.
func coveredByMoves(
	compare context.Context,
	ledger tools.SourceHistory,
	tree tools.SourceTree,
	moves []displayMove,
	changes []workspacebaseline.Change,
	recorded map[string]bool,
) (map[string]bool, error) {
	if len(moves) == 0 {
		return nil, nil
	}
	created := make(map[string]bool, len(changes))
	for _, change := range changes {
		if change.Kind == workspacebaseline.ChangeCreated && !recorded[change.Path] {
			created[change.Path] = true
		}
	}
	var candidates []workspacebaseline.PathMove
	for _, change := range changes {
		if change.Kind != workspacebaseline.ChangeDeleted || recorded[change.Path] {
			continue
		}
		if to := throughMoves(moves, change.Path); to != change.Path && created[to] {
			candidates = append(candidates, workspacebaseline.PathMove{From: change.Path, To: to})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	unchanged, err := awaitTree(compare, func(ctx context.Context) (map[workspacebaseline.PathMove]bool, error) {
		return ledger.Tree.TreeMovesUnchanged(ctx, tree, candidates)
	})
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool, 2*len(unchanged))
	for move := range unchanged {
		covered[move.From], covered[move.To] = true, true
	}
	return covered, nil
}
