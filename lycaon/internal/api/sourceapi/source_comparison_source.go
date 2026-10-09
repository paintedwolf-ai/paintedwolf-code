package sourceapi

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// loadComparisonSource resolves exact endpoints independently of their display.
// Retained view references are resolved by the view service before calling it.
// A batch may pass pre-read commit trees; a single read passes none.
func (s *Comparisons) loadComparisonSource(ctx context.Context, p *project.Project, sessionID string, source wire.SourceComparisonSelector, trees *commitTrees) (wire.SourceComparison, error) {
	if err := source.Validate(); err != nil {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "The comparison selector must name exactly one source."}
	}
	if source.Text != nil {
		return loadTextComparison(*source.Text)
	}
	if source.Chat != nil {
		return s.loadChatComparison(ctx, p.ID, sessionID, *source.Chat)
	}
	if source.GitRange != nil {
		diff, err := s.Review.loadGitRangeComparison(ctx, p, *source.GitRange)
		if err != nil {
			return wire.SourceComparison{}, err
		}
		return mapUnscreenedSourceComparison(diff), nil
	}
	var diff sourceledger.Comparison
	var err error
	switch {
	case source.Effect != nil:
		diff, err = s.SourceLedger.CompareEffect(ctx, p.ID, source.Effect.EffectID)
	case source.Version != nil:
		diff, err = s.SourceLedger.CompareVersions(ctx, p.ID, source.Version.VersionID)
	case source.Scope != nil:
		diff, err = s.loadScopeComparison(ctx, p, *source.Scope)
	case source.Turn != nil:
		diff, err = s.loadTurnComparison(ctx, p, *source.Turn)
	case source.Reviewed != nil:
		diff, err = s.loadReviewedComparison(ctx, p, *source.Reviewed)
	case source.Commit != nil:
		diff, err = s.loadCommitComparison(ctx, p, *source.Commit, trees)
	case source.Blob != nil:
		diff, err = s.loadBlobComparison(ctx, p, *source.Blob)
	case source.GitChange != nil:
		diff, err = s.loadGitChangeComparison(ctx, p, *source.GitChange)
	default:
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A retained comparison requires an active source view."}
	}
	if err != nil {
		return wire.SourceComparison{}, err
	}
	return mapUnscreenedSourceComparison(diff), nil
}

func loadTextComparison(source wire.TextComparisonSource) (wire.SourceComparison, error) {
	if source.Path == "" {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A text comparison requires a path."}
	}
	before, after := TextComparisonSide(source.Path, source.Before), TextComparisonSide(source.Path, source.After)
	return comparisonText(before, after)
}

func TextComparisonSide(path string, text *string) wire.SourceComparisonSide {
	if text == nil {
		return wire.SourceComparisonSide{Path: path, State: "absent", Availability: "absent"}
	}
	return readerTextSide(path, *text)
}

func (s *Comparisons) loadChatComparison(ctx context.Context, projectID, sessionID string, source wire.ChatComparisonSource) (wire.SourceComparison, error) {
	if sessionID == "" {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A chat comparison requires a session."}
	}
	session, err := s.SessionStore.Get(ctx, sessionID)
	if err == nil && (session == nil || session.ProjectID != projectID) {
		// A chat outside this project is absent from it.
		err = store.ErrSessionNotFound
	}
	if errors.Is(err, store.ErrSessionNotFound) {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSessionNotFound, "Chat not found."}
	}
	if err != nil {
		return wire.SourceComparison{}, err
	}
	first, err := s.readChatFileEdit(ctx, sessionID, source.First)
	if err != nil {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSourceEffectNotFound, "File edit not found."}
	}
	last, err := s.readChatFileEdit(ctx, sessionID, source.Last)
	if err != nil || first.Path != last.Path || first.RootID != last.RootID {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSourceEffectNotFound, "File edit not found."}
	}
	old := ""
	if first.Before != nil {
		old = *first.Before
	}
	before, after := readerTextSide(first.Path, old), readerTextSide(last.Path, last.After)
	before.RootID, after.RootID = first.RootID, last.RootID
	if first.Before == nil {
		before.State, before.Availability = "absent", "absent"
	}
	if last.Deleted {
		after.State, after.Availability = "absent", "absent"
	}
	if (source.ExpectedBeforeSHA256 != "" && source.ExpectedBeforeSHA256 != sourcecomparison.Hash(before.Content)) ||
		(source.ExpectedAfterSHA256 != "" && source.ExpectedAfterSHA256 != sourcecomparison.Hash(after.Content)) {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionChanged, "The retained file edit changed. Reopen it to continue."}
	}
	return comparisonText(before, after)
}

func comparisonText(before, after wire.SourceComparisonSide) (wire.SourceComparison, error) {
	if len(before.Content) > projectsource.SourceReadMaxBytes || len(after.Content) > projectsource.SourceReadMaxBytes {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSourceTextTooLarge, "The source exceeds the supported text size."}
	}
	return wire.SourceComparison{InRange: true, Before: &before, After: &after}, nil
}
