package sourceview

import (
	"context"
	"errors"
	"net/url"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

// Provenance is the ledger read surface tool handlers reach by
// asserting tctx.SourceLedger.
type Provenance interface {
	ResolveHead(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (sourceledger.BranchHead, error)
	LatestFileEffect(ctx context.Context, projectID, fileID string) (sourceledger.Effect, bool, error)
	SessionActivityFloor(ctx context.Context, projectID, sessionID string) (int64, bool, error)
	QueryFileEffects(ctx context.Context, projectID, fileID string, afterOrdinal, beforeOrdinal int64, limit int) (sourceledger.FileEffectsResult, error)
}

const (
	sourceStampSHAShortLen = 12
	StampTimeLayout        = "2006-01-02 15:04 UTC"
	// editForeignChangeRows bounds the provenance rows an edit reject carries.
	editForeignChangeRows = 3
)

// LedgerLocation resolves a path to its branch, root, and relative path.
func LedgerLocation(tctx tools.ToolContext, absPath string) (branch sourcebranch.ID, rootID, rel string, ok bool) {
	root, path, located := tctx.SourceLocation(absPath)
	if tctx.Identity.ProjectID == "" || !located {
		return sourcebranch.Trunk, "", "", false
	}
	branch, branchErr := tctx.SourceBranch(root.ID)
	if branchErr != nil {
		return sourcebranch.Trunk, "", "", false
	}
	return branch, root.ID, path, true
}

// ReadStamp returns nil outside ledger scope and marks failed lookups as unrecorded.
func ReadStamp(
	ctx context.Context,
	tctx tools.ToolContext,
	absPath, servedSHA256 string,
) *surveyreceipt.SourceContext {
	tctx.RecordSourcePath(absPath, api.NavigationEntryKindFile)
	prov, ok := tctx.Source.SourceLedger.(Provenance)
	if !ok {
		return nil
	}
	branch, rootID, rel, ok := LedgerLocation(tctx, absPath)
	if !ok {
		return nil
	}
	destination := url.URL{Scheme: "source", Host: rootID, Path: "/" + rel}
	if tctx.Source.WorkerBranchRoot != "" && tctx.Identity.WorkerJobID != "" {
		destination.RawQuery = url.Values{"job_id": {tctx.Identity.WorkerJobID}}.Encode()
	}
	stamp := &surveyreceipt.SourceContext{SHA256Short: ShortSHA(servedSHA256), Navigation: destination.String()}
	head, err := prov.ResolveHead(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		stamp.Note = "no recorded history for this path"
		return stamp
	}
	if err != nil {
		stamp.Note = "ledger unavailable for this read"
		return stamp
	}
	if head.SHA256 != "" && head.SHA256 == servedSHA256 {
		stamp.Recorded = true
		stamp.VersionID = head.VersionID
	} else {
		stamp.Note = "served bytes are newer than the last recorded state"
	}
	if latest, found, err := prov.LatestFileEffect(ctx, tctx.Identity.ProjectID, head.FileID); err == nil && found {
		stamp.LastChange = ChangeOf(latest, tctx.Identity.SessionID)
	}
	return stamp
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

// ForeignChanges returns other actors' recorded changes, newest first; ok reports lookup completeness.
func ForeignChanges(
	ctx context.Context,
	tctx tools.ToolContext,
	absPath string,
) (changes []*surveyreceipt.SourceChange, total int, ok bool) {
	prov, isProv := tctx.Source.SourceLedger.(Provenance)
	if !isProv || tctx.Identity.SessionID == "" {
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
	page, err := prov.QueryFileEffects(ctx, tctx.Identity.ProjectID, head.FileID, floor, 0, 50)
	if err != nil {
		return nil, 0, false
	}
	for _, effect := range page.Effects {
		if effect.BranchID != branch || effect.ActorClassFor(tctx.Identity.SessionID) == sourceledger.ActorYou {
			continue
		}
		total++
		if len(changes) < editForeignChangeRows {
			changes = append(changes, ChangeOf(effect, tctx.Identity.SessionID))
		}
	}
	return changes, total, len(changes) > 0
}

func ShortSHA(sha string) string {
	if len(sha) <= sourceStampSHAShortLen {
		return sha
	}
	return sha[:sourceStampSHAShortLen]
}
