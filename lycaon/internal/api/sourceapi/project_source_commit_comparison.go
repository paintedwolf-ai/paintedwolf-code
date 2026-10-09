package sourceapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type commitWorkingState struct {
	Side sourceledger.ComparisonSide
	OIDs sourceblob.GitOIDs
}

func readWorkingCommit(p *project.Project, rootID, rootAbs, path string) commitWorkingState {
	state := commitWorkingState{}
	side := sourceledger.ComparisonSide{RootID: rootID, Path: path, State: "unresolved", Availability: sourceledger.ContentUnavailable}
	if !filepath.IsLocal(path) || path == "." {
		side.Reason = "path_unavailable"
		state.Side = side
		return state
	}
	abs, rel, ok := evidence.ResolveCitationAbs(rootAbs, path)
	if !ok || rel != path {
		side.Reason = "path_unavailable"
		state.Side = side
		return state
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		side.State, side.Availability = "absent", sourceledger.ContentAbsent
		state.Side = side
		return state
	}
	if err != nil {
		side.Reason = "content_unavailable"
		state.Side = side
		return state
	}
	if !info.Mode().IsRegular() {
		side.Reason = "non_regular_file"
		state.Side = side
		return state
	}
	observation, err := project.ObserveProjectSource(p, project.SourceReadRequest{RootID: rootID, Path: path})
	if err != nil {
		side.Reason = "content_unavailable"
		state.Side = side
		return state
	}
	if !observation.OverLimit {
		state.OIDs = sourceblob.ContentGitOIDs(observation.Revision.Bytes)
	}
	side.State, side.SHA256, side.SizeBytes = "content", observation.Revision.SHA256, observation.SizeBytes
	if observation.OverLimit {
		side.Availability, side.Reason = sourceledger.ContentNotCaptured, "content_too_large"
		state.Side = side
		return state
	}
	content, err := observation.Project()
	if err != nil {
		side.Reason = "content_unavailable"
		state.Side = side
		return state
	}
	if content.Binary {
		side.Availability, side.Reason = sourceledger.ContentBinary, "binary_content"
		state.Side = side
		return state
	}
	side.Availability, side.Content = sourceledger.ContentAvailable, content.Content
	state.Side = side
	return state
}

func (s *Review) writeCommitPathComparison(w http.ResponseWriter, r *http.Request, p *project.Project) {
	q := r.URL.Query()
	rootID, path := q.Get("root_id"), q.Get("path")
	if q.Get("baseline") != "commit" || rootID == "" || !filepath.IsLocal(path) || path == "." {
		s.responses.InvalidQueryParam(w, "baseline", "must be commit, with root_id and a local path")
		return
	}
	var expected *string
	if q.Has("expected_head") {
		value := q.Get("expected_head")
		expected = &value
	}
	diff, err := s.Comparisons.loadCommitComparison(r.Context(), p, wire.CommitComparisonSource{RootID: rootID, Path: path, ExpectedHead: expected}, nil)
	if err != nil {
		s.Comparisons.writeComparisonError(w, r, err)
		return
	}
	s.Comparisons.writeSourceComparison(w, r, p.ID, diff)
}
