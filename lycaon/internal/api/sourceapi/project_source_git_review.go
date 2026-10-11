package sourceapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceGitReviewRequest struct {
	mgr              git.GitManager
	rootID           string
	rootAbs          string
	change           sourceledger.GitTransition
	commit           git.CommitDetails
	commitComparison bool
	opts             git.CommitReviewOptions
}

func commitReviewDefault(kind string) bool {
	switch wire.SourceGitChangeKind(kind) {
	case wire.SourceGitChangeCommit, wire.SourceGitChangeAmend, wire.SourceGitChangeMerge,
		wire.SourceGitChangeCherryPick, wire.SourceGitChangeRevert, wire.SourceGitChangeClone:
		return true
	default:
		return false
	}
}

func (s *Review) sourceGitReviewRequest(w http.ResponseWriter, r *http.Request) (sourceGitReviewRequest, bool) {
	var empty sourceGitReviewRequest
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return empty, false
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return empty, false
	}
	var movement *bool
	if value, present, err := httpio.OptionalBoolQuery(r, "movement"); err != nil {
		s.responses.InvalidQuery(w, err)
		return empty, false
	} else if present {
		movement = &value
	}
	var parent *int
	if value, present, err := httpio.OptionalIntQuery(r, "parent", 1, 1000); err != nil {
		s.responses.InvalidQuery(w, err)
		return empty, false
	} else if present {
		parent = &value
	}
	req, err := s.Comparisons.loadSourceGitReview(r.Context(), p, chi.URLParam(r, "git_change_id"), movement, parent)
	if err != nil {
		s.Comparisons.writeComparisonError(w, r, err)
		return empty, false
	}
	return req, true
}

func (s *Review) HandleGetProjectSourceGitReview(w http.ResponseWriter, r *http.Request) {
	req, ok := s.sourceGitReviewRequest(w, r)
	if !ok {
		return
	}
	input, ok := s.readReviewPageInput(w, r, gitReviewPages, req.rootID, req.opts.Before, req.opts.After)
	if !ok {
		return
	}
	page, nextCursor, ok := s.readReviewPage(w, r, gitReviewPages, req, input)
	if !ok {
		return
	}
	out := wire.SourceGitReview{
		Change: *mapSourceGitChange(req.change),
		Commit: wire.SourceGitCommitDetails{Hash: req.commit.Hash, Parents: append([]string{}, req.commit.Parents...), Message: req.commit.Message,
			AuthorName: req.commit.AuthorName, AuthoredAt: req.commit.AuthoredAt, CommittedAt: req.commit.CommittedAt},
		CommitComparison: req.commitComparison, BeforeCommit: req.opts.Before, AfterCommit: req.opts.After,
		Files: mapGitReviewFiles(page.Files), FilesTotal: page.Total, Insertions: page.Insertions, Deletions: page.Deletions, NextCursor: nextCursor,
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// reviewPosition is the file offset into one commit pair's review.
type reviewPosition struct {
	Offset int `json:"offset"`
}

var (
	gitReviewPages      = pagecursor.For[reviewPosition]("source_git_review")
	revisionReviewPages = pagecursor.For[reviewPosition]("source_revision_review")
	reviewPageLimit     = httpio.MustPageLimit(100, 1, 500)
)

// reviewPageInput is a validated page request into one commit pair's review.
type reviewPageInput struct {
	scope         string
	limit, offset int
}

// readReviewPageInput validates `cursor` and `limit` before the commit pair is
// resolved. Cursors bind to the pair and root, which fully determine the
// listed files.
func (s *Review) readReviewPageInput(
	w http.ResponseWriter,
	r *http.Request,
	pages pagecursor.Codec[reviewPosition],
	rootID, before, after string,
) (reviewPageInput, bool) {
	pq, err := httpio.ReadPageQuery(r, reviewPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return reviewPageInput{}, false
	}
	input := reviewPageInput{scope: pagecursor.Scope(rootID, before, after), limit: pq.Limit}
	if pq.Cursor != "" {
		position, err := pages.Decode(pq.Cursor, input.scope)
		if err == nil && position.Offset <= 0 {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return reviewPageInput{}, false
		}
		input.offset = position.Offset
	}
	return input, true
}

// readReviewPage reads one page of the commit pair req names.
func (s *Review) readReviewPage(
	w http.ResponseWriter,
	r *http.Request,
	pages pagecursor.Codec[reviewPosition],
	req sourceGitReviewRequest,
	input reviewPageInput,
) (git.CommitReviewPage, string, bool) {
	req.opts.Limit, req.opts.Offset = input.limit, input.offset
	page, err := req.mgr.CommitReview(r.Context(), req.rootAbs, req.opts)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable, "The Git comparison could not be read from this repository.")
		return git.CommitReviewPage{}, "", false
	}
	var nextCursor string
	if page.NextOffset > 0 {
		nextCursor, err = pages.Encode(input.scope, reviewPosition{Offset: page.NextOffset})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return git.CommitReviewPage{}, "", false
		}
	}
	return page, nextCursor, true
}

func mapGitReviewFiles(files []git.CommitReviewFile) []wire.SourceGitReviewFile {
	out := make([]wire.SourceGitReviewFile, 0, len(files))
	for _, file := range files {
		out = append(out, mapGitReviewFile(file))
	}
	return out
}

func mapGitReviewFile(file git.CommitReviewFile) wire.SourceGitReviewFile {
	op := wire.SourceChangeOpWrite
	switch file.Status {
	case "A", "C":
		op = wire.SourceChangeOpCreate
	case "D":
		op = wire.SourceChangeOpDelete
	case "R":
		op = wire.SourceChangeOpRename
	}
	return wire.SourceGitReviewFile{Path: file.Path, BeforePath: file.BeforePath, Op: op,
		BeforeMode: file.BeforeMode, AfterMode: file.AfterMode, BeforeOid: file.BeforeOID, AfterOid: file.AfterOID,
		Insertions: file.Insertions, Deletions: file.Deletions, Binary: file.Binary}
}

func gitReviewComparisonSide(ctx context.Context, req sourceGitReviewRequest, oid, mode, path string) sourceledger.ComparisonSide {
	side := gitReviewObjectSide(ctx, req, oid, mode)
	side.RootID, side.Path = req.rootID, path
	return side
}

func gitReviewObjectSide(ctx context.Context, req sourceGitReviewRequest, oid, mode string) sourceledger.ComparisonSide {
	if mode == "160000" {
		return sourceledger.ComparisonSide{State: "content", Availability: sourceledger.ContentUnavailable, Reason: "gitlink"}
	}
	if oid != "" {
		size, err := req.mgr.BlobSize(ctx, req.rootAbs, oid)
		if err != nil {
			return sourceledger.ComparisonSide{State: "content", Availability: sourceledger.ContentUnavailable, Reason: "content_unavailable"}
		}
		if size > sourceledger.MaxRevisionContentBytes {
			return sourceledger.ComparisonSide{State: "content", SizeBytes: size, Availability: sourceledger.ContentNotCaptured, Reason: "content_too_large"}
		}
	}
	return gitBlobComparisonSide(ctx, req.mgr, req.rootAbs, oid)
}
