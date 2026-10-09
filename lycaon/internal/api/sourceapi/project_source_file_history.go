package sourceapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Arrival anchoring is bounded by calls and elapsed time.
const (
	arrivalWindowSpan      = 12
	arrivalAncestryBudget  = 40
	arrivalAnchorTimeBound = 2 * time.Second
)

// fileGitLane joins one lineage page to retained versions and arrivals.
func (s *Review) fileGitLane(
	ctx context.Context,
	p *project.Project,
	fileID string,
	head sourceledger.BranchHead,
	gitSkip int,
) (
	commits []wire.SourceFileCommit,
	arrivals []wire.SourceGitChange,
	state wire.SourceGitHistoryState,
	nextSkip int,
) {
	started := time.Now()
	var lineageDuration, projectionDuration time.Duration
	defer func() {
		slog.InfoContext(ctx, "source file history projection", "project_id", p.ID, "file_id", fileID,
			"duration_ms", time.Since(started).Milliseconds(), "lineage_ms", lineageDuration.Milliseconds(),
			"projection_ms", projectionDuration.Milliseconds(), "state", state, "commits", len(commits))
	}()
	commits = []wire.SourceFileCommit{}
	arrivals = []wire.SourceGitChange{}
	mgr := s.Git.Manager()
	if head.RootID == "" || head.Path == "" {
		return commits, arrivals, wire.SourceGitHistoryStateNotTracked, 0
	}
	rootAbs, _, ok := s.Comparisons.resolveRootRepoPosition(ctx, p, head.RootID)
	if !ok {
		return commits, arrivals, wire.SourceGitHistoryStateNoRepository, 0
	}
	lineageStarted := time.Now()
	lineage, err := mgr.FileHistory(ctx, rootAbs, git.GitFileHistoryOpts{
		Path: head.Path, Skip: gitSkip, Limit: git.DefaultFileHistoryPage,
	})
	lineageDuration = time.Since(lineageStarted)
	if err != nil {
		return commits, arrivals, gitLaneFailureState(err), 0
	}
	projectionStarted := time.Now()
	defer func() { projectionDuration = time.Since(projectionStarted) }()
	oids, err := s.SourceLedger.History.FileVersionGitOIDs(ctx, p.ID, fileID)
	if err != nil {
		oids = nil
	}
	anchorCtx, cancelAnchor := context.WithTimeout(ctx, arrivalAnchorTimeBound)
	defer cancelAnchor()
	anchor := s.newArrivalAnchor(anchorCtx, mgr, rootAbs, p.ID, head.RootID)
	seenArrivals := map[string]struct{}{}
	for _, entry := range lineage {
		row := wire.SourceFileCommit{
			Commit:      entry.Hash,
			Subject:     entry.Subject,
			AuthoredAt:  entry.AuthoredAt,
			CommittedAt: entry.CommittedAt,
			SourcePath:  entry.Path,
		}
		if entry.AuthorName != "" {
			author := entry.AuthorName
			row.AuthorName = &author
		}
		if entry.BlobOID != "" {
			oid := entry.BlobOID
			row.BlobOid = &oid
		}
		if versionID, held := oids[entry.BlobOID]; held && entry.BlobOID != "" {
			id := versionID
			row.MatchesVersionID = &id
		} else if transition, found := anchor.arrivalOf(entry.Hash); found {
			id := transition.ID
			row.ArrivalGitChangeID = &id
			if _, listed := seenArrivals[transition.ID]; !listed {
				seenArrivals[transition.ID] = struct{}{}
				arrivals = append(arrivals, *mapSourceGitChange(transition))
			}
		}
		commits = append(commits, row)
	}
	if len(lineage) == git.DefaultFileHistoryPage &&
		gitSkip+len(lineage) < git.MaxFileHistoryDepth {
		nextSkip = gitSkip + len(lineage)
	}
	return commits, arrivals, wire.SourceGitHistoryStateAvailable, nextSkip
}

// gitLaneFailureState distinguishes timeouts from other failures.
func gitLaneFailureState(err error) wire.SourceGitHistoryState {
	if errors.Is(err, git.ErrFileHistoryTimeout) {
		return wire.SourceGitHistoryStateTimedOut
	}
	return wire.SourceGitHistoryStateFailed
}

// arrivalAnchor finds the first observed head that reaches a commit.
type arrivalAnchor struct {
	ctx     context.Context
	mgr     git.GitManager
	rootAbs string
	chain   []sourceledger.GitTransition
	memo    map[string]bool
	budget  int
}

func (s *Review) newArrivalAnchor(
	ctx context.Context,
	mgr git.GitManager,
	rootAbs, projectID, rootID string,
) *arrivalAnchor {
	chain, err := s.SourceLedger.History.GitTransitionChain(ctx, projectID, rootID, 64)
	if err != nil {
		chain = nil
	}
	return &arrivalAnchor{
		ctx: ctx, mgr: mgr, rootAbs: rootAbs, chain: chain,
		memo: map[string]bool{}, budget: arrivalAncestryBudget,
	}
}

func (a *arrivalAnchor) isAncestor(commit, head string) (bool, bool) {
	if head == "" {
		return false, true
	}
	key := commit + ":" + head
	if held, ok := a.memo[key]; ok {
		return held, true
	}
	if a.budget <= 0 {
		return false, false
	}
	a.budget--
	held, err := a.mgr.IsAncestor(a.ctx, a.rootAbs, commit, head)
	if err != nil {
		return false, false
	}
	a.memo[key] = held
	return held, true
}

// arrivalOf returns the first newly reachable window in the bounded chain.
func (a *arrivalAnchor) arrivalOf(commit string) (sourceledger.GitTransition, bool) {
	start, pivot, bounded := a.boundedStart()
	if !bounded {
		return sourceledger.GitTransition{}, false
	}
	// Anything the pivot already reached was present before this scope opened.
	reached, known := a.isAncestor(commit, pivot)
	if !known || reached {
		return sourceledger.GitTransition{}, false
	}
	for i := start; i < len(a.chain); i++ {
		window := a.chain[i]
		if window.ToCommit == "" {
			continue
		}
		reached, known := a.isAncestor(commit, window.ToCommit)
		if !known {
			return sourceledger.GitTransition{}, false
		}
		if reached {
			return window, true
		}
	}
	return sourceledger.GitTransition{}, false
}

// boundedStart finds the earliest window with an observed lower commit bound.
func (a *arrivalAnchor) boundedStart() (int, string, bool) {
	start := len(a.chain) - arrivalWindowSpan
	if start < 0 {
		start = 0
	}
	for i := start; i < len(a.chain); i++ {
		if from := a.chain[i].FromCommit; from != "" {
			return i, from, true
		}
	}
	return 0, "", false
}

// writeSourceBlobComparison compares verified repository objects.
func (s *Review) writeSourceBlobComparison(
	w http.ResponseWriter,
	r *http.Request,
	p *project.Project,
	rootID, afterOID, beforeOID string,
) {
	diff, err := s.Comparisons.loadBlobComparison(r.Context(), p, wire.BlobComparisonSource{
		RootID: rootID, BlobOid: afterOID, BeforeBlobOid: beforeOID, DisplayPath: r.URL.Query().Get("display_path"),
	})
	if err != nil {
		s.Comparisons.writeComparisonError(w, r, err)
		return
	}
	s.Comparisons.writeSourceComparison(w, r, p.ID, diff)
}

func gitBlobComparisonSide(
	ctx context.Context,
	mgr git.GitManager,
	rootAbs, blobOID string,
) sourceledger.ComparisonSide {
	if blobOID == "" {
		return sourceledger.ComparisonSide{State: "absent", Availability: sourceledger.ContentAbsent}
	}
	raw, ok := fetchVerifiedGitBlob(ctx, mgr, rootAbs, blobOID)
	if !ok {
		return sourceledger.ComparisonSide{
			State: "content", Availability: sourceledger.ContentUnavailable,
			Reason: "content_unavailable",
		}
	}
	side := sourceledger.ComparisonSide{
		State: "content", SHA256: textfile.SHA256(raw), SizeBytes: int64(len(raw)),
	}
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(projectsource.SourceReadMaxBytes))
	if err != nil {
		side.Availability, side.Reason = sourceledger.ContentBinary, "binary_content"
		return side
	}
	side.Availability, side.Content = sourceledger.ContentAvailable, doc.Text()
	return side
}

// fetchVerifiedGitBlob verifies the object's content address.
func fetchVerifiedGitBlob(
	ctx context.Context,
	mgr git.GitManager,
	rootAbs, blobOID string,
) ([]byte, bool) {
	raw, ok, err := mgr.BlobContent(ctx, rootAbs, blobOID, sourceledger.MaxRevisionContentBytes)
	if err != nil || !ok {
		return nil, false
	}
	derived := sourceblob.ContentGitOIDs(raw)
	if derived.SHA1 != blobOID && derived.SHA256 != blobOID {
		return nil, false
	}
	return raw, true
}

// commitSHAPattern admits abbreviated and full commit ids.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// HandleRestoreProjectSourceCommitState restores a verified commit blob.
func (s *Mutations) HandleRestoreProjectSourceCommitState(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	commit := strings.TrimSpace(chi.URLParam(r, "commit_sha"))
	if !commitSHAPattern.MatchString(commit) {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "commit_sha must be 7 to 64 lowercase hexadecimal characters")
		return
	}
	var req wire.SourceCommitRestoreRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	blobOID := strings.TrimSpace(req.BlobOid)
	sourcePath, sourcePathOK := commitSourcePath(strings.TrimSpace(req.SourcePath))
	if strings.TrimSpace(req.FileID) == "" || strings.TrimSpace(req.RootID) == "" ||
		strings.TrimSpace(req.Path) == "" || blobOID == "" || !sourcePathOK {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest,
			"file_id, root_id, path, source_path, and blob_oid are required")
		return
	}
	mgr := s.Git.Manager()
	rootAbs, prefix, mapped := s.Comparisons.resolveRootRepoPosition(r.Context(), p, strings.TrimSpace(req.RootID))
	if !mapped {
		s.responses.Fail(w, wire.ApiErrorCodeSourceCommitNotFound, "no git repository serves this root")
		return
	}
	exists, err := mgr.CommitExists(r.Context(), rootAbs, commit)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if !exists {
		s.responses.Fail(w, wire.ApiErrorCodeSourceCommitNotFound, "the repository has no such commit")
		return
	}
	if prefix != "" {
		if !strings.HasPrefix(sourcePath, prefix) {
			s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable,
				"the commit's bytes are unreachable or no longer match the listing")
			return
		}
		sourcePath = strings.TrimPrefix(sourcePath, prefix)
	}
	oids, treeErr := mgr.TreeOIDs(r.Context(), rootAbs, commit, []string{sourcePath})
	if treeErr != nil || oids[sourcePath] != blobOID {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable,
			"the commit's bytes are unreachable or no longer match the listing")
		return
	}
	raw, verified := fetchVerifiedGitBlob(r.Context(), mgr, rootAbs, blobOID)
	if !verified {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable,
			"the commit's bytes are unreachable or no longer match the listing")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Versions.Restore(r.Context(), operationID.String(), p, projectsource.SourceVersionRestoreRequest{
		Version: sourceledger.RestorableVersion{
			FileID: strings.TrimSpace(req.FileID), ProjectID: p.ID,
			RootID: strings.TrimSpace(req.RootID), Path: req.Path,
			State:  string(wire.SourceTipStateContent),
			SHA256: textfile.SHA256(raw), Content: raw,
		},
		Commit: commit,
		FileID: strings.TrimSpace(req.FileID), RootID: strings.TrimSpace(req.RootID),
		Path: req.Path, Base: req.Base, SessionID: sessionID, Turn: turn,
	})
	switch {
	case errors.Is(err, sourceledger.ErrHistoryNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourceFileNotFound, "logical file not found")
		return
	case errors.Is(err, sourceledger.ErrVersionUnavailable):
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable,
			"the commit's bytes are unreachable or no longer match the listing")
		return
	case err != nil:
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceCommitRestoreResponse{
		Commit: commit, PreviousVersionID: result.PreviousVersionID,
		FileID: result.FileID, RootID: result.RootID, Path: result.Path,
		State: result.State, Sha256: result.SHA256, Changed: result.Changed,
	})
}

func commitSourcePath(raw string) (string, bool) {
	clean := path.Clean(raw)
	if raw == "" || clean != raw || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") ||
		strings.ContainsRune(clean, 0) {
		return "", false
	}
	return clean, true
}
