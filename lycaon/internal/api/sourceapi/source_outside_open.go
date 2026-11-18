package sourceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleOpenOutsideFile adds a file the person picked to a chat's locations.
func (s *Workspace) HandleOpenOutsideFile(w http.ResponseWriter, r *http.Request) {
	var req wire.OpenOutsideFileRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, chi.URLParam(r, "id"))
	if release == nil {
		return
	}
	defer release()
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sess, err := s.SessionStore.Get(r.Context(), strings.TrimSpace(req.SessionID))
	if err != nil || sess.ProjectID != p.ID || sess.ParentSessionID != "" || sess.OwnerPersonID != requestscope.Caller(r).ID {
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found in this project")
		return
	}
	scoped, ok := requestscope.ProjectForSession(s.SessionStore, s.responses, w, r, p, sess.ID)
	if !ok {
		return
	}
	abs := fspath.CanonicalPath(req.Path)
	if abs == "" || !filepath.IsAbs(req.Path) {
		s.responses.InvalidField(w, "path", "must be an absolute host path")
		return
	}
	if pathInsideAttached(p, abs) || scoped != p && pathInsideAttached(scoped, abs) {
		s.responses.InvalidField(w, "path", "is inside a project folder")
		return
	}
	rel, ok := projectroot.OutsideRel(abs)
	if !ok || p.Outside.ID == "" {
		s.responses.InvalidField(w, "path", "has no outside-project address on this platform")
		return
	}
	file, err := projectsource.OpenOutsideSourceFile(rel)
	if err != nil {
		s.writeOutsideFileOpenError(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()
	s.trackOpenedOutsideFile(w, r, scoped, sess.ID, rel, file, nil)
}

func (s *Workspace) trackOpenedOutsideFile(w http.ResponseWriter, r *http.Request, p *project.Project, chatID, rel string, file *os.File, location *sourceledger.ChatLocation) {
	observed, err := observeOutsideFile(file)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	observed.ProjectID, observed.RootID, observed.Path = p.ID, p.Outside.ID, rel
	var tracked sourceledger.TrackedFile
	if location == nil {
		tracked, err = s.SourceLedger.TrackChatFile(r.Context(), observed, chatID, sourceledger.ChatFileOpened)
	} else {
		tracked, err = s.SourceLedger.TrackChatLocationFile(r.Context(), observed, *location)
	}
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
		return
	}
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	if location == nil {
		covering, found, err := s.SourceLedger.ChatLocationCovering(r.Context(), chatID, rel)
		if err != nil {
			s.writeSourceReadError(w, r, err)
			return
		}
		if !found {
			s.writeOutsideFileOpenError(w, r, projectsource.ErrSourceNotFound)
			return
		}
		location = &covering
	}
	notifyChatLocationChange(r, p)
	httpio.WriteJSON(w, http.StatusOK, wire.OpenedOutsideFile{RootID: p.Outside.ID, Path: rel, FileID: tracked.FileID, Location: chatLocationDTO(p, *location)})
}

// Large files contribute a hash without retained bytes.
func observeOutsideFile(f *os.File) (sourceledger.TrackInput, error) {
	info, err := f.Stat()
	if err != nil {
		return sourceledger.TrackInput{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceledger.TrackInput{}, projectsource.ErrSourceIsDirectory
	}
	if info.Size() <= sourceblob.MaxRevisionContentBytes {
		content, err := io.ReadAll(io.LimitReader(f, sourceblob.MaxRevisionContentBytes+1))
		if err != nil {
			return sourceledger.TrackInput{}, err
		}
		return sourceledger.TrackInput{Content: content, Size: int64(len(content))}, nil
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return sourceledger.TrackInput{}, err
	}
	return sourceledger.TrackInput{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

// HandleUpdateSourceChatLocation hides or shows a location.
func (s *Workspace) HandleUpdateSourceChatLocation(w http.ResponseWriter, r *http.Request) {
	var req wire.UpdateSourceChatLocationRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "location_id"))
	location, err := s.SourceLedger.ChatLocation(r.Context(), p.ID, id)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	chat, err := s.SessionStore.Get(r.Context(), location.SessionID)
	if err != nil || chat.OwnerPersonID != requestscope.Caller(r).ID {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
		return
	}
	if req.Hidden != nil {
		if err := s.SourceLedger.HideChatLocation(r.Context(), p.ID, id, *req.Hidden); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		location.Hidden = *req.Hidden
		notifyChatLocationChange(r, p)
	}
	httpio.WriteJSON(w, http.StatusOK, chatLocationDTO(p, location))
}

func (s *Workspace) HandleOpenChatLocationFile(w http.ResponseWriter, r *http.Request) {
	var req wire.OpenChatLocationFileRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !filepath.IsLocal(req.Path) || filepath.Clean(req.Path) != req.Path || req.Path == "." {
		s.responses.InvalidField(w, "path", "must name a file relative to the location")
		return
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, chi.URLParam(r, "id"))
	if release == nil {
		return
	}
	defer release()
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	location, err := s.SourceLedger.ChatLocation(r.Context(), p.ID, chi.URLParam(r, "location_id"))
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	chat, err := s.SessionStore.Get(r.Context(), location.SessionID)
	if err != nil || chat.OwnerPersonID != requestscope.Caller(r).ID {
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
		return
	}
	scoped, ok := requestscope.ProjectForSession(s.SessionStore, s.responses, w, r, p, chat.ID)
	if !ok {
		return
	}
	file, err := openChatLocationFile(location, req.Path)
	if err != nil {
		s.writeOutsideFileOpenError(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()
	if pathInsideAttached(p, file.Name()) || scoped != p && pathInsideAttached(scoped, file.Name()) {
		s.responses.InvalidField(w, "path", "is inside a project folder")
		return
	}
	rel, ok := projectroot.OutsideRel(file.Name())
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotFound, "file not found")
		return
	}
	s.trackOpenedOutsideFile(w, r, scoped, chat.ID, rel, file, &location)
}

func openChatLocationFile(location sourceledger.ChatLocation, path string) (*os.File, error) {
	if location.Kind == sourceledger.ChatLocationFile {
		if filepath.Base(location.Anchor) != path {
			return nil, projectsource.ErrSourcePathDenied
		}
	} else if confine.ClassifyBlockedWrite(filepath.Join(location.Directory(), path)).Kind != confine.WriteSubjectOrdinary {
		return nil, projectsource.ErrSourcePathDenied
	}
	root, err := fseffect.OpenReadRoot(location.Directory())
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if root.Path() != location.Directory() {
		return nil, projectsource.ErrSourceNotFound
	}
	held, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	identity, err := fspath.FileIdentity(held)
	if err != nil {
		return nil, err
	}
	if identity != location.AnchorIdentity {
		return nil, projectsource.ErrSourceNotFound
	}
	return root.OpenCanonicalFile(path)
}

func (s *Workspace) writeOutsideFileOpenError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, projectsource.ErrSourceNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotFound, "file not found")
	case errors.Is(err, os.ErrPermission), errors.Is(err, fseffect.ErrSymlink), errors.Is(err, fseffect.ErrPredicateChanged):
		s.writeSourceReadError(w, r, projectsource.ErrSourcePathDenied)
	default:
		s.writeSourceReadError(w, r, err)
	}
}

func pathInsideAttached(p *project.Project, abs string) bool {
	for _, root := range p.Roots {
		rel, err := filepath.Rel(fspath.CanonicalPath(root.Path), abs)
		if err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

func notifyChatLocationChange(r *http.Request, p *project.Project) {
	if err := sourcefeed.EmitProjectSignal(r.Context(), p.ID, project.RootRefsFrom(p)); err != nil {
		slog.Debug("Chat location event was not published", "project", p.ID, "error", err)
	}
}
