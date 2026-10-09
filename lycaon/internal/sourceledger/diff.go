package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrHistoryNotFound identifies a missing effect or version.
var ErrHistoryNotFound = errors.New("source history not found")

type ContentAvailability string

const (
	ContentAvailable   ContentAvailability = "available"
	ContentAbsent      ContentAvailability = "absent"
	ContentDirectory   ContentAvailability = "directory"
	ContentUnavailable ContentAvailability = "unavailable"
	ContentNotCaptured ContentAvailability = "not_captured"
	ContentBinary      ContentAvailability = "binary"
	ContentUnresolved  ContentAvailability = "unresolved"
)

// VersionID is empty for working-file and repository-object endpoints.
type ComparisonSide struct {
	VersionID    string
	RootID       string
	Path         string
	State        string
	SHA256       string
	SizeBytes    int64
	Availability ContentAvailability
	Reason       string
	Content      string
}

type Comparison struct {
	PresentationAfterOrdinal *int64
	Attribution              *ComparisonAttribution
	InRange                  bool
	EffectID                 string
	FileID                   string
	Op                       api.SourceChangeOp
	Before                   ComparisonSide
	After                    ComparisonSide
	LocationChanged          bool
	// UserEditsUnmarked reports that authorship marks omit the person's edits.
	UserEditsUnmarked bool
}

// ScopeComparisonOptions selects what a range comparison marks.
type ScopeComparisonOptions struct {
	// PresentationAfterOrdinal pins an open view to its original unread boundary.
	PresentationAfterOrdinal *int64
	// UnmarkUserEdits suppresses the person's authored ranges.
	UnmarkUserEdits bool
}

// CompareEffect reads an effect's immutable endpoints.
func (s *Comparisons) CompareEffect(ctx context.Context, projectID, effectID string) (Comparison, error) {
	if s == nil {
		return Comparison{}, fmt.Errorf("ledger not configured")
	}
	effect, err := s.queries.GetSourceEffect(ctx, effectID)
	if errors.Is(err, sql.ErrNoRows) {
		return Comparison{}, ErrHistoryNotFound
	}
	if err != nil {
		return Comparison{}, err
	}
	if effect.ProjectID != projectID {
		return Comparison{}, ErrHistoryNotFound
	}
	out, err := s.compareVersions(ctx, projectID, effect.BeforeVersionID, effect.AfterVersionID)
	if err != nil {
		return Comparison{}, err
	}
	out.InRange, out.EffectID, out.FileID = true, effect.ID, effect.FileID
	out.Op = api.SourceChangeOp(effect.Op)
	out.LocationChanged = out.Before.RootID != out.After.RootID || out.Before.Path != out.After.Path
	if err := s.attachComparisonAttribution(ctx, Baseline{}, ScopeComparisonOptions{}, &out); err != nil {
		return Comparison{}, err
	}
	return out, nil
}

// CompareVersions compares a version with its parent.
func (s *Comparisons) CompareVersions(ctx context.Context, projectID, versionID string) (Comparison, error) {
	version, err := s.queries.GetSourceVersion(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Comparison{}, ErrHistoryNotFound
	}
	if err != nil {
		return Comparison{}, err
	}
	if version.ProjectID != projectID {
		return Comparison{}, ErrHistoryNotFound
	}
	out, err := s.compareVersions(ctx, projectID, version.ParentVersionID, version.ID)
	if err != nil {
		return Comparison{}, err
	}
	out.InRange, out.FileID = true, version.FileID
	out.LocationChanged = out.Before.RootID != out.After.RootID || out.Before.Path != out.After.Path
	if err := s.attachComparisonAttribution(ctx, Baseline{}, ScopeComparisonOptions{}, &out); err != nil {
		return Comparison{}, err
	}
	return out, nil
}

// CompareScope compares a range start with its tracked head.
func (s *Comparisons) CompareScope(
	ctx context.Context,
	projectID string,
	branch sourcebranch.ID,
	baseline Baseline,
	fileID string,
	opts ScopeComparisonOptions,
) (Comparison, error) {
	head, err := s.queries.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
		ProjectID: projectID, BranchID: branch.String(), FileID: fileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Comparison{}, nil
	}
	if err != nil {
		return Comparison{}, err
	}
	if baseline.Kind == BaselineCommit {
		return Comparison{}, fmt.Errorf("commit comparison requires a working path")
	}
	var beforeVersionID string
	ordinal := int64(0)
	switch baseline.Kind {
	case BaselineSession:
		row, rowErr := s.queries.OldestSourceEffectForFileInSession(ctx,
			db.OldestSourceEffectForFileInSessionParams{ProjectID: projectID, FileID: head.FileID, SessionID: baseline.SessionID})
		if errors.Is(rowErr, sql.ErrNoRows) {
			return Comparison{}, nil
		}
		if rowErr != nil {
			return Comparison{}, rowErr
		}
		beforeVersionID = row.BeforeVersionID
	case BaselineTurn:
		row, rowErr := s.queries.OldestSourceEffectForFileInTurn(ctx,
			db.OldestSourceEffectForFileInTurnParams{ProjectID: projectID, FileID: head.FileID, SessionID: baseline.SessionID, Turn: int64(baseline.Turn)})
		if errors.Is(rowErr, sql.ErrNoRows) {
			return Comparison{}, nil
		}
		if rowErr != nil {
			return Comparison{}, rowErr
		}
		beforeVersionID = row.BeforeVersionID
	default:
		switch baseline.Kind {
		case BaselinePin:
			ordinal, err = s.walk.resolvePinOrdinal(ctx, projectID, baseline)
			if err != nil {
				return Comparison{}, err
			}
		case BaselinePresentation:
			// Only changes after the acknowledged version belong to this range.
			watermark, watermarkErr := s.queries.GetSourcePresentationWatermark(ctx,
				db.GetSourcePresentationWatermarkParams{ProjectID: projectID, FileID: head.FileID})
			if watermarkErr == nil {
				ordinal = watermark.ThroughOrdinal
			} else if !errors.Is(watermarkErr, sql.ErrNoRows) {
				return Comparison{}, watermarkErr
			}
			if opts.PresentationAfterOrdinal != nil {
				ordinal = *opts.PresentationAfterOrdinal
			}
		case BaselineTurn, BaselineSession, BaselineCommit:
		}
		row, rowErr := s.queries.OldestSourceEffectForFileAfterOrdinal(ctx,
			db.OldestSourceEffectForFileAfterOrdinalParams{ProjectID: projectID, FileID: head.FileID, Ordinal: ordinal})
		if errors.Is(rowErr, sql.ErrNoRows) {
			return Comparison{}, nil
		}
		if rowErr != nil {
			return Comparison{}, rowErr
		}
		beforeVersionID = row.BeforeVersionID
	}
	out, err := s.compareVersions(ctx, projectID, beforeVersionID, head.VersionID)
	if err != nil {
		return Comparison{}, err
	}
	if baseline.Kind == BaselinePresentation {
		out.PresentationAfterOrdinal = &ordinal
	}
	out.InRange, out.FileID = true, head.FileID
	out.LocationChanged = out.Before.RootID != out.After.RootID || out.Before.Path != out.After.Path
	if err := s.attachComparisonAttribution(ctx, baseline, opts, &out); err != nil {
		return Comparison{}, err
	}
	out.UserEditsUnmarked = opts.UnmarkUserEdits && out.Attribution != nil

	return out, nil
}

// CompareTurn compares one file from its state before the turn to its state
// after the turn's last write, excluding later turns.
func (s *Comparisons) CompareTurn(ctx context.Context, projectID, sessionID string, turn int, fileID string, opts ScopeComparisonOptions) (Comparison, error) {
	if s == nil {
		return Comparison{}, fmt.Errorf("ledger not configured")
	}
	first, err := s.queries.OldestSourceEffectForFileInTurn(ctx, db.OldestSourceEffectForFileInTurnParams{
		ProjectID: projectID, FileID: fileID, SessionID: sessionID, Turn: int64(turn),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Comparison{}, nil
	}
	if err != nil {
		return Comparison{}, err
	}
	last, err := s.queries.LatestSourceEffectForFileInTurn(ctx, db.LatestSourceEffectForFileInTurnParams{
		ProjectID: projectID, FileID: fileID, SessionID: sessionID, Turn: int64(turn),
	})
	if err != nil {
		return Comparison{}, err
	}
	out, err := s.compareVersions(ctx, projectID, first.BeforeVersionID, last.AfterVersionID)
	if err != nil {
		return Comparison{}, err
	}
	out.InRange, out.FileID = true, fileID
	// A single write is that effect, so line facts address it as the walk does.
	if first.ID == last.ID {
		out.EffectID = last.ID
	}
	out.LocationChanged = out.Before.RootID != out.After.RootID || out.Before.Path != out.After.Path
	baseline := Baseline{Kind: BaselineTurn, SessionID: sessionID, Turn: turn}
	if err := s.attachComparisonAttribution(ctx, baseline, opts, &out); err != nil {
		return Comparison{}, err
	}
	out.UserEditsUnmarked = opts.UnmarkUserEdits && out.Attribution != nil
	return out, nil
}

// CompareVersionPair compares two arbitrary retained states.
func (s *Comparisons) CompareVersionPair(ctx context.Context, projectID, beforeID, afterID string) (Comparison, error) {
	return s.compareVersions(ctx, projectID, beforeID, afterID)
}

func (s *Comparisons) compareVersions(ctx context.Context, projectID, beforeID, afterID string) (Comparison, error) {
	before, err := s.comparisonSide(ctx, projectID, beforeID)
	if err != nil {
		return Comparison{}, err
	}
	after, err := s.comparisonSide(ctx, projectID, afterID)
	if err != nil {
		return Comparison{}, err
	}
	return Comparison{Before: before, After: after}, nil
}

func (s *Comparisons) comparisonSide(ctx context.Context, projectID, versionID string) (ComparisonSide, error) {
	if versionID == "" {
		return ComparisonSide{State: "absent", Availability: ContentAbsent}, nil
	}
	version, err := s.queries.GetSourceVersion(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	if err != nil {
		return ComparisonSide{}, err
	}
	if version.ProjectID != projectID {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	out := ComparisonSide{VersionID: version.ID, RootID: version.RootID, Path: version.Path,
		State: version.State, SHA256: version.ContentSha256, SizeBytes: version.ByteSize,
		Reason: version.CaptureReason}
	switch version.State {
	case "absent":
		out.Availability = ContentAbsent
		return out, nil
	case "directory":
		out.Availability = ContentDirectory
		return out, nil
	case "unresolved":
		out.Availability = ContentUnresolved
		return out, nil
	}
	if version.CaptureState != "stored" {
		out.Availability = ContentNotCaptured
		return out, nil
	}
	raw, found, err := s.retention.readVerifiedBlob(ctx, version.ContentSha256)
	if err != nil {
		return ComparisonSide{}, err
	}
	if !found {
		out.Availability, out.Reason = ContentUnavailable, "content_unavailable"
		return out, nil
	}
	text, ok := decodeTextSnapshot(raw)
	if !ok {
		out.Availability, out.Reason = ContentBinary, "binary_content"
		return out, nil
	}
	out.Availability, out.Content = ContentAvailable, text
	return out, nil
}

// Comparison resolves retained content and authorship for review and rewind.
type Comparisons struct {
	queries   *db.Queries
	recordMu  *sync.Mutex
	sqlDB     db.Handle
	history   comparisonsHistoryPort
	retention comparisonsRetentionPort
	walk      comparisonsWalkPort
}

type comparisonsWalkPort interface {
	resolvePinOrdinal(ctx context.Context, projectID string, baseline Baseline) (int64, error)
}

type comparisonsRetentionPort interface {
	readVerifiedBlob(ctx context.Context, sha256 string) ([]byte, bool, error)
}

type comparisonsHistoryPort interface {
	ReadRestorableVersion(
		ctx context.Context,
		projectID, versionID string,
	) (RestorableVersion, error)
	ResolveHeadByFile(
		ctx context.Context,
		projectID string, branch sourcebranch.ID, fileID string,
	) (BranchHead, error)
}
