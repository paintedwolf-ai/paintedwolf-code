package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
)

type ChatLocationKind string

const (
	ChatLocationRepository ChatLocationKind = "repository"
	ChatLocationFolder     ChatLocationKind = "folder"
	ChatLocationFile       ChatLocationKind = "file"
)

// ChatLocation uses an outside-namespace path as its anchor.
type ChatLocation struct {
	ID, SessionID, Anchor, AnchorIdentity string
	Kind                                  ChatLocationKind
	Hidden                                bool
}

// Covers reports whether the outside path rel lies at or under the location.
func (l ChatLocation) Covers(rel string) bool {
	if l.Kind == ChatLocationFile {
		return rel == l.Anchor
	}
	return rel == l.Anchor || strings.HasPrefix(rel, l.Anchor+"/")
}

// Relative is rel inside the location, "." for its anchor.
func (l ChatLocation) Relative(rel string) string {
	if rel == l.Anchor {
		return "."
	}
	return strings.TrimPrefix(rel, l.Anchor+"/")
}

// Protected files and files at the filesystem root are their own anchors.
func locationAnchor(rel string) (string, ChatLocationKind) {
	abs := projectroot.OutsideAbs(rel)
	if confine.ClassifyBlockedWrite(abs).Kind != confine.WriteSubjectOrdinary {
		return rel, ChatLocationFile
	}
	grant := confine.FloorRecovery{Grant: confine.GrantRepositoryTree}.GrantPath(abs)
	anchor, ok := projectroot.OutsideRel(grant)
	if !ok || anchor == rel {
		return rel, ChatLocationFile
	}
	if _, err := os.Lstat(filepath.Join(grant, ".git")); err == nil {
		return anchor, ChatLocationRepository
	}
	return anchor, ChatLocationFolder
}

// Directory is the folder whose identity the location holds.
func (l ChatLocation) Directory() string {
	host := projectroot.OutsideAbs(l.Anchor)
	if l.Kind == ChatLocationFile {
		return filepath.Dir(host)
	}
	return host
}

// Available checks that the location still holds its recorded folder.
func (l ChatLocation) Available() bool {
	if l.AnchorIdentity == "" {
		return false
	}
	identity, err := projectroot.OutsideDirectoryIdentity(l.Directory())
	return err == nil && identity == l.AnchorIdentity
}

func chatLocationFromRow(row db.SourceChatLocations) ChatLocation {
	return ChatLocation{ID: row.ID, SessionID: row.SessionID, Anchor: row.Anchor, AnchorIdentity: row.AnchorIdentity, Kind: ChatLocationKind(row.Kind), Hidden: row.Hidden != 0}
}

func placeChatLocation(ctx context.Context, q *db.Queries, chat, rel string, relation ChatFileRelation, ts time.Time) error {
	anchor, kind := locationAnchor(rel)
	rows, err := q.ListSourceChatLocations(ctx, chat)
	if err != nil {
		return err
	}
	for _, row := range rows {
		location := chatLocationFromRow(row)
		if !location.Covers(rel) || kind == ChatLocationFile && location.Kind != ChatLocationFile {
			continue
		}
		if !location.Available() {
			continue
		}
		if location.Hidden && relation&(ChatFileChanged|ChatFileOpened) != 0 {
			return q.SetSourceChatLocationHidden(ctx, db.SetSourceChatLocationHiddenParams{Hidden: 0, ID: location.ID})
		}
		return nil
	}
	location := ChatLocation{Anchor: anchor, Kind: kind}
	identity, _ := projectroot.OutsideDirectoryIdentity(location.Directory())
	return q.InsertSourceChatLocation(ctx, db.InsertSourceChatLocationParams{
		ID: uuid.NewString(), SessionID: chat, Anchor: anchor, AnchorIdentity: identity, Kind: string(kind), CreatedTs: ts.UTC().Format(time.RFC3339Nano),
	})
}

// ChatLocations omits folders covered by a visible ancestor.
func (s *Store) ChatLocations(ctx context.Context, sessionID string) ([]ChatLocation, error) {
	chat, err := s.queries.GetRootSessionID(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSourceChatLocations(ctx, chat)
	if err != nil {
		return nil, err
	}
	all := make([]ChatLocation, 0, len(rows))
	folders := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		location := chatLocationFromRow(row)
		all = append(all, location)
		if !location.Hidden && location.Kind != ChatLocationFile && location.Available() {
			folders[location.Anchor] = struct{}{}
		}
	}
	locations := make([]ChatLocation, 0, len(all))
	for _, location := range all {
		if location.Kind == ChatLocationFile || !hasAncestorLocation(location.Anchor, folders) {
			locations = append(locations, location)
		}
	}
	return locations, nil
}

func hasAncestorLocation(anchor string, folders map[string]struct{}) bool {
	for parent := path.Dir(anchor); parent != "."; parent = path.Dir(parent) {
		if _, ok := folders[parent]; ok {
			return true
		}
	}
	return false
}

// ChatLocationCovering answers the chat's visible location covering rel.
func (s *Store) ChatLocationCovering(ctx context.Context, sessionID, rel string) (ChatLocation, bool, error) {
	locations, err := s.ChatLocations(ctx, sessionID)
	if err != nil {
		return ChatLocation{}, false, err
	}
	for _, location := range locations {
		if location.Kind == ChatLocationFile && !location.Hidden && location.Covers(rel) && location.Available() {
			return location, true, nil
		}
	}
	for _, location := range locations {
		if !location.Hidden && location.Covers(rel) && location.Available() {
			return location, true, nil
		}
	}
	return ChatLocation{}, false, nil
}

// ChatLocation answers one location of the project.
func (s *Store) ChatLocation(ctx context.Context, projectID, id string) (ChatLocation, error) {
	row, err := s.queries.GetSourceChatLocation(ctx, db.GetSourceChatLocationParams{ID: id, ProjectID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		return ChatLocation{}, ErrHistoryNotFound
	}
	if err != nil {
		return ChatLocation{}, err
	}
	return chatLocationFromRow(row), nil
}

func (s *Store) HideChatLocation(ctx context.Context, projectID, id string, hidden bool) error {
	if _, err := s.ChatLocation(ctx, projectID, id); err != nil {
		return err
	}
	value := int64(0)
	if hidden {
		value = 1
	}
	return s.queries.SetSourceChatLocationHidden(ctx, db.SetSourceChatLocationHiddenParams{Hidden: value, ID: id})
}

// TrackChatFile records the file and chat reference atomically.
func (s *Store) TrackChatFile(ctx context.Context, raw TrackInput, sessionID string, relation ChatFileRelation) (TrackedFile, error) {
	return s.trackFile(ctx, raw, func(ctx context.Context, q *db.Queries, in TrackInput, tracked TrackedFile) error {
		return s.referenceChatFile(ctx, q, in.ProjectID, in.RootID, in.Path, sessionID, tracked.FileID, relation, in.TS)
	})
}

// TrackChatLocationFile opens an existing location without rebinding its anchor.
func (s *Store) TrackChatLocationFile(ctx context.Context, raw TrackInput, location ChatLocation) (TrackedFile, error) {
	return s.trackFile(ctx, raw, func(ctx context.Context, q *db.Queries, in TrackInput, tracked TrackedFile) error {
		row, err := q.GetSourceChatLocation(ctx, db.GetSourceChatLocationParams{ProjectID: in.ProjectID, ID: location.ID})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHistoryNotFound
		}
		if err != nil {
			return err
		}
		current := chatLocationFromRow(row)
		if current.AnchorIdentity != location.AnchorIdentity || !current.Covers(in.Path) || !current.Available() {
			return ErrHistoryNotFound
		}
		return s.referenceChatFile(ctx, q, in.ProjectID, in.RootID, "", current.SessionID, tracked.FileID, ChatFileOpened, in.TS)
	})
}
