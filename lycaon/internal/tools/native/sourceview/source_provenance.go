package sourceview

import (
	"context"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

const (
	sourceStampSHAShortLen = 12
	StampTimeLayout        = "2006-01-02 15:04 UTC"
	// editForeignChangeRows bounds the provenance rows an edit reject carries.
	editForeignChangeRows = 3
	foreignChangePageSize = 200
)

// LedgerLocation resolves a path to its branch, root, and relative path.
func LedgerLocation(tctx tools.ToolContext, absPath string) (branch sourcebranch.ID, rootID, rel string, ok bool) {
	root, path, located := tctx.RecordedLocation(absPath)
	if tctx.Identity.ProjectID == "" || !located {
		return sourcebranch.Trunk, "", "", false
	}
	branch, branchErr := tctx.SourceBranch(root.ID)
	if branchErr != nil {
		return sourcebranch.Trunk, "", "", false
	}
	return branch, root.ID, path, true
}

func ChangeOf(effect sourceledger.Effect, viewerSessionID string) *surveyreceipt.SourceChange {
	change := &surveyreceipt.SourceChange{
		Actor:  string(effect.ActorClassFor(viewerSessionID)),
		Detail: effect.ActorDisplay(viewerSessionID),
		Op:     string(effect.Op),
		At:     effect.TS.UTC().Format(StampTimeLayout),
	}
	change.Turn = effect.AuthoredTurn()
	return change
}

// ForeignChanges returns other actors' recorded changes, newest first; ok reports
// lookup completeness. Effects inside the session's own command windows are not
// claimed as another actor's.
func ForeignChanges(
	ctx context.Context,
	tctx tools.ToolContext,
	absPath string,
) (changes []*surveyreceipt.SourceChange, total int, ok bool) {
	prov := tctx.Source.History.Files
	if prov == nil || tctx.Identity.SessionID == "" {
		return nil, 0, false
	}
	branch, rootID, rel, located := LedgerLocation(tctx, absPath)
	if !located {
		return nil, 0, false
	}
	head, err := prov.ResolveHead(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
	if err != nil {
		return nil, 0, false
	}
	floor, found, err := prov.SessionActivityFloor(ctx, tctx.Identity.ProjectID, tctx.Identity.SessionID)
	if err != nil || !found {
		return nil, 0, false
	}
	// The session's own effects can only be told apart here, so read every page:
	// they must not push a foreign effect out of the count.
	for before := int64(0); ; {
		page, err := prov.QueryFileEffects(ctx, tctx.Identity.ProjectID, branch, head.FileID, floor, before, foreignChangePageSize)
		if err != nil {
			return nil, 0, false
		}
		for _, effect := range page.Effects {
			if effect.ActorClassFor(tctx.Identity.SessionID) == sourceledger.ActorYou || effect.DuringCommandOf(tctx.Identity.SessionID) {
				continue
			}
			total++
			if len(changes) < editForeignChangeRows {
				changes = append(changes, ChangeOf(effect, tctx.Identity.SessionID))
			}
		}
		if page.NextBeforeOrdinal == 0 {
			return changes, total, len(changes) > 0
		}
		before = page.NextBeforeOrdinal
	}
}

func ShortSHA(sha string) string {
	if len(sha) <= sourceStampSHAShortLen {
		return sha
	}
	return sha[:sourceStampSHAShortLen]
}
