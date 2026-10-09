package sourceledger

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// DefaultSeenFiles is one Seen page when the reader asks for no size.
	DefaultSeenFiles = 25
	// MaxSeenFiles bounds one Seen page.
	MaxSeenFiles = 100
	// maxSeenEffects bounds the effects listed for one look.
	maxSeenEffects = 100
)

// SeenFile is a file whose latest look is still current, with the effects
// that look covered.
type SeenFile struct {
	FileID, RootID, Path string
	Tip                  Tip
	SeenAt               time.Time
	// ThroughOrdinal identifies the look targeted by withdrawal.
	ThroughOrdinal   int64
	Effects          []Effect
	EffectsTruncated bool
}

// SeenPageQuery continues a Seen listing after one look. Empty AfterSeenTS
// opens the newest page.
type SeenPageQuery struct {
	Limit                    int
	AfterSeenTS, AfterFileID string
}

// SeenResult is one Seen page. NextSeenTS and NextFileID name the last look
// the page read, including one it did not list; both are empty on the last page.
type SeenResult struct {
	Files                  []SeenFile
	NextSeenTS, NextFileID string
}

type seenRow struct {
	fileID, seenTS, rootID, path, state, contentSHA256 string
	seenAfter, through                                 int64
}

// QuerySeen lists the files the reader has looked at, newest look first.
// Without user edits, the person's own writes neither make a file new again
// nor count among the effects a look covered.
func (s *History) QuerySeen(
	ctx context.Context,
	projectID string,
	rootBranches map[string]sourcebranch.ID,
	page SeenPageQuery,
	withoutUserEdits bool,
) (SeenResult, error) {
	if s == nil {
		return SeenResult{}, fmt.Errorf("ledger not configured")
	}
	limit := page.Limit
	if limit <= 0 || limit > MaxSeenFiles {
		limit = DefaultSeenFiles
	}
	rows, err := s.seenRows(ctx, projectID, rootBranches, page, int64(limit+1), withoutUserEdits)
	if err != nil {
		return SeenResult{}, err
	}
	var out SeenResult
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		out.NextSeenTS, out.NextFileID = last.seenTS, last.fileID
	}
	out.Files = make([]SeenFile, 0, len(rows))
	for _, row := range rows {
		file, listed, err := s.seenFile(ctx, projectID, row, withoutUserEdits)
		if err != nil {
			return SeenResult{}, err
		}
		if listed {
			out.Files = append(out.Files, file)
		}
	}
	return out, nil
}

func (s *History) seenRows(ctx context.Context, projectID string, roots map[string]sourcebranch.ID, page SeenPageQuery, limit int64, withoutUserEdits bool) ([]seenRow, error) {
	rootBranches, err := encodeRootBranches(roots)
	if err != nil {
		return nil, err
	}
	var out []seenRow
	if withoutUserEdits {
		rows, err := s.queries.ListSourcePresentationSeenWithoutUserEdits(ctx,
			db.ListSourcePresentationSeenWithoutUserEditsParams{
				ProjectID: projectID, RootBranches: rootBranches, PageLimit: limit,
				AfterSeenTs: page.AfterSeenTS, AfterFileID: page.AfterFileID,
			})
		for _, r := range rows {
			out = append(out, seenRow{r.FileID, r.SeenTs, r.RootID, r.Path, r.State, r.ContentSha256, r.SeenAfterOrdinal, r.ThroughOrdinal})
		}
		return out, err
	}
	rows, err := s.queries.ListSourcePresentationSeen(ctx,
		db.ListSourcePresentationSeenParams{
			ProjectID: projectID, RootBranches: rootBranches, PageLimit: limit,
			AfterSeenTs: page.AfterSeenTS, AfterFileID: page.AfterFileID,
		})
	for _, r := range rows {
		out = append(out, seenRow{r.FileID, r.SeenTs, r.RootID, r.Path, r.State, r.ContentSha256, r.SeenAfterOrdinal, r.ThroughOrdinal})
	}
	return out, err
}

// seenFile reports false for a head that is neither content nor a deletion,
// which no editor presents.
func (s *History) seenFile(ctx context.Context, projectID string, row seenRow, withoutUserEdits bool) (SeenFile, bool, error) {
	file := SeenFile{FileID: row.fileID, RootID: row.rootID, Path: row.path, ThroughOrdinal: row.through}
	switch row.state {
	case "content":
		file.Tip = Tip{State: api.SourceTipStateContent, SHA256: row.contentSHA256}
	case "absent":
		file.Tip = Tip{State: api.SourceTipStateAbsent}
	default:
		return SeenFile{}, false, nil
	}
	file.SeenAt, _ = time.Parse(time.RFC3339Nano, row.seenTS)
	rows, err := s.queries.ListSourceEffectsForFileRange(ctx, db.ListSourceEffectsForFileRangeParams{
		ProjectID: projectID, FileID: row.fileID,
		AfterOrdinal: row.seenAfter, ThroughOrdinal: row.through, PageLimit: maxSeenEffects + 1,
	})
	if err != nil {
		return SeenFile{}, false, err
	}
	if len(rows) > maxSeenEffects {
		rows, file.EffectsTruncated = rows[:maxSeenEffects], true
	}
	for _, r := range rows {
		file.Effects = append(file.Effects, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID,
			r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath,
			r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
			r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
			r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	if err := s.walk.hydrateEffectAuthors(ctx, projectID, file.Effects); err != nil {
		return SeenFile{}, false, err
	}
	if withoutUserEdits {
		file.Effects = slices.DeleteFunc(file.Effects, func(e Effect) bool {
			return e.onlyUserContributions()
		})
	}
	return file, true, nil
}
