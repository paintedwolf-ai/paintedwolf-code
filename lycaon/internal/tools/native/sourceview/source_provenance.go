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

const (
	sourceStampSHAShortLen = 12
	StampTimeLayout        = "2006-01-02 15:04 UTC"
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
	prov := tctx.Source.History.Files
	if prov == nil {
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
	if latest, found, err := prov.LatestFileEffect(ctx, tctx.Identity.ProjectID, branch, head.FileID); err == nil && found {
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

func ShortSHA(sha string) string {
	if len(sha) <= sourceStampSHAShortLen {
		return sha
	}
	return sha[:sourceStampSHAShortLen]
}
