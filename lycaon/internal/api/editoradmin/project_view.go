package editoradmin

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/viewstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleReplaceProjectView records what one of the caller's windows shows of the project.
func (s *Handler) HandleReplaceProjectView(w http.ResponseWriter, r *http.Request) {
	var req wire.ReplaceProjectViewRequest
	var raw map[string]any
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "client_id", "focused") {
		return
	}
	if field, reason := invalidProjectView(req); field != "" {
		s.responses.InvalidField(w, field, reason)
		return
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	err := s.ProjectViews.Replace(p.ID, requestscope.Caller(r).ID, req)
	if errors.Is(err, viewstate.ErrClientIdentity) {
		s.responses.Fail(w, wire.ApiErrorCodeEditorReplicaIdentity, "This window belongs to another person.")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func invalidProjectView(req wire.ReplaceProjectViewRequest) (field, reason string) {
	validID := func(id string) bool { _, err := uuid.Parse(id); return err == nil }
	validPath := func(path string) bool {
		return path != "" && len(path) <= 4096 && !strings.HasPrefix(path, "/") && !strings.Contains("/"+path+"/", "/../")
	}
	location := func(rootID, path string, line int) bool { return validID(rootID) && validPath(path) && line >= 0 }
	switch {
	case strings.TrimSpace(req.ClientID) == "" || len(req.ClientID) > 256:
		return "client_id", "is required and at most 256 characters"
	case req.Editor != nil && !validID(req.Editor.DocumentID):
		return "editor.document_id", "must be a document identity"
	case req.Diff != nil && !location(req.Diff.RootID, req.Diff.Path, 0):
		return "diff", "must name a root and a relative path"
	case req.Folder != nil && !location(req.Folder.RootID, req.Folder.Path, 0):
		return "folder", "must name a root and a relative path"
	case req.Search != nil && (strings.TrimSpace(req.Search.Query) == "" || len(req.Search.Query) > 1024):
		return "search.query", "is required and at most 1024 bytes"
	case req.Search != nil && req.Search.Hit != nil && !location(req.Search.Hit.RootID, req.Search.Hit.Path, req.Search.Hit.Line):
		return "search.hit", "must name a root, a relative path and a line"
	case len(req.Findings) > 8:
		return "findings", "holds at most 8 findings"
	}
	for _, f := range req.Findings {
		if f.Fingerprint == "" || len(f.Fingerprint) > 256 || f.RuleID == "" || len(f.RuleID) > 128 || !slices.Contains(wire.AllFindingLevelValues(), f.Level) {
			return "findings", "each needs a fingerprint, a rule and a level"
		}
		if f.Location != nil && !location(f.Location.RootID, f.Location.Path, f.Location.Line) {
			return "findings", "a location must name a root, a relative path and a line"
		}
	}
	return "", ""
}
