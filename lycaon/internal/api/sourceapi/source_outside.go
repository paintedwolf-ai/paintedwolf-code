package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// chatLocationDTO is a location as the wire names it.
func chatLocationDTO(p *project.Project, location sourceledger.ChatLocation) wire.SourceChatLocation {
	host := projectroot.OutsideAbs(location.Anchor)
	label := filepath.Base(host)
	if location.Kind == sourceledger.ChatLocationFile {
		label = filepath.Base(filepath.Dir(host))
	}
	return wire.SourceChatLocation{
		ID: location.ID, SessionID: location.SessionID, RootID: p.Outside.ID, Anchor: location.Anchor,
		Path: host, Kind: string(location.Kind), Label: label, Hidden: location.Hidden, Available: location.Available(),
	}
}

// chatLocationRoot is the tree root a location lists on demand. A file
// location is its folder listing only that file.
func chatLocationRoot(dto wire.SourceChatLocation, identity string) sourcetree.Root {
	if identity == "" {
		identity = "-"
	}
	root := sourcecatalog.Root{ID: dto.ID, Path: dto.Path, OnDemand: true, DirectoryIdentity: identity}
	if dto.Kind == string(sourceledger.ChatLocationFile) {
		root.Path, root.Only = filepath.Dir(dto.Path), filepath.Base(dto.Path)
	}
	return sourcetree.Root{Root: root, Label: dto.Label}
}

type chatLocationProjection struct {
	details []wire.SourceChatLocation
	roots   []sourcetree.Root
}

func (s *Trees) chatLocations(ctx context.Context, scope pagedview.Scope, p *project.Project, chatID string) (chatLocationProjection, error) {
	if chatID == "" {
		return chatLocationProjection{}, nil
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return chatLocationProjection{}, rejectedSourceIntent("chat_id must be a UUID.")
	}
	chat, err := s.Views.SessionStore.Get(ctx, chatID)
	if err != nil {
		return chatLocationProjection{}, err
	}
	if chat.ProjectID != scope.Project || chat.OwnerPersonID != scope.Person || chat.ParentSessionID != "" {
		return chatLocationProjection{}, store.ErrSessionNotFound
	}
	if s.SourceLedger == nil || p.Outside.ID == "" {
		return chatLocationProjection{}, nil
	}
	locations, err := s.SourceLedger.ChatLocations(ctx, chatID)
	if err != nil {
		return chatLocationProjection{}, err
	}
	var out chatLocationProjection
	for _, location := range locations {
		if !location.Hidden {
			dto := chatLocationDTO(p, location)
			out.details = append(out.details, dto)
			out.roots = append(out.roots, chatLocationRoot(dto, location.AnchorIdentity))
		}
	}
	return out, nil
}

func (n sourceReadNavigation) outsideLocations() []wire.SourceChatLocation {
	if n.outside == nil {
		return []wire.SourceChatLocation{}
	}
	return n.outside
}

// refreshChatLocations re-reads the folders of the view's locations that
// changes landed in, since no watcher covers them.
func (s *Trees) refreshChatLocations(view *sourceView, outsideRootID string, changed []sourcefeed.Address) {
	for _, location := range view.outsideLocations() {
		var dirs []string
		for _, address := range changed {
			if address.RootID != outsideRootID {
				continue
			}
			ledger := sourceledger.ChatLocation{Anchor: location.Anchor, Kind: sourceledger.ChatLocationKind(location.Kind)}
			if !ledger.Covers(address.Path) {
				continue
			}
			dir := path.Dir(ledger.Relative(address.Path))
			if location.Kind == string(sourceledger.ChatLocationFile) {
				dir = "."
			}
			dirs = append(dirs, dir)
		}
		view.navigation.tree.RefreshOnDemandDirectories(location.ID, dirs)
	}
}

// addAbsentLocationPath marks a deleted outside file in each of the view's
// locations covering it.
func addAbsentLocationPath(locations []wire.SourceChatLocation, builder *sourcetree.ReviewBuilder, rel string) error {
	if _, err := os.Lstat(projectroot.OutsideAbs(rel)); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	for _, location := range locations {
		covering := sourceledger.ChatLocation{Anchor: location.Anchor, Kind: sourceledger.ChatLocationKind(location.Kind)}
		if !covering.Covers(rel) {
			continue
		}
		inside := covering.Relative(rel)
		if covering.Kind == sourceledger.ChatLocationFile {
			inside = path.Base(rel)
		}
		if err := builder.Add(sourcetree.Address{Root: location.ID, Path: inside}); err != nil {
			return err
		}
	}
	return nil
}

// HandleOpenOutsideFile adds a file the person picked to a chat's locations.
func (s *Workspace) HandleOpenOutsideFile(w http.ResponseWriter, r *http.Request) {
	var req wire.OpenOutsideFileRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sess, err := s.SessionStore.Get(r.Context(), strings.TrimSpace(req.SessionID))
	if err != nil || sess.ProjectID != p.ID || sess.ParentSessionID != "" || sess.OwnerPersonID != requestscope.Caller(r).ID {
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found in this project")
		return
	}
	abs := fspath.CanonicalPath(strings.TrimSpace(req.Path))
	if abs == "" || !filepath.IsAbs(req.Path) {
		s.responses.InvalidField(w, "path", "must be an absolute host path")
		return
	}
	for _, root := range p.Roots {
		if rel, err := filepath.Rel(fspath.CanonicalPath(root.Path), abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			s.responses.InvalidField(w, "path", "is inside a project folder")
			return
		}
	}
	rel, ok := projectroot.OutsideRel(abs)
	if !ok || p.Outside.ID == "" {
		s.responses.InvalidField(w, "path", "has no outside-project address on this platform")
		return
	}
	observed, err := observeOutsideFile(rel)
	if errors.Is(err, projectsource.ErrSourceNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotFound, "file not found")
		return
	}
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	observed.ProjectID, observed.RootID, observed.Path = p.ID, p.Outside.ID, rel
	tracked, err := s.SourceLedger.TrackChatFile(r.Context(), observed, sess.ID, sourceledger.ChatFileOpened)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	location, found, err := s.SourceLedger.ChatLocationCovering(r.Context(), sess.ID, rel)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	if !found {
		s.responses.InternalError(w, r, errors.New("opened outside file has no location"))
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.OpenedOutsideFile{RootID: p.Outside.ID, Path: rel, FileID: tracked.FileID, Location: chatLocationDTO(p, location)})
}

// observeOutsideFile hashes a picked file, keeping its bytes only when the
// ledger retains content of that size.
func observeOutsideFile(rel string) (sourceledger.TrackInput, error) {
	f, err := projectsource.OpenOutsideSourceFile(rel)
	if err != nil {
		return sourceledger.TrackInput{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return sourceledger.TrackInput{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceledger.TrackInput{}, projectsource.ErrSourceIsDirectory
	}
	if info.Size() <= sourceledger.MaxRevisionContentBytes {
		content, err := io.ReadAll(io.LimitReader(f, sourceledger.MaxRevisionContentBytes+1))
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
	if req.Hidden != nil {
		if err := s.SourceLedger.HideChatLocation(r.Context(), p.ID, id, *req.Hidden); errors.Is(err, sourceledger.ErrHistoryNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
			return
		} else if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	location, err := s.SourceLedger.ChatLocation(r.Context(), p.ID, id)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "location not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, chatLocationDTO(p, location))
}
