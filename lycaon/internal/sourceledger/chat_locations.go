package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ChatLocationKind is what a chat location is anchored at.
type ChatLocationKind string

const (
	ChatLocationRepository ChatLocationKind = "repository"
	ChatLocationFolder     ChatLocationKind = "folder"
	ChatLocationFile       ChatLocationKind = "file"
)

// ChatLocation is a place outside the project where a root chat has worked.
// Anchor is an outside-namespace path.
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

// locationAnchor applies the write-root approval's subject rule to an outside
// file: its repository work tree, else its folder. A protected file, or one
// directly under the filesystem root, is its own anchor.
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

// placeChatLocation makes the location covering rel visible for the chat,
// adding one when none covers it.
func placeChatLocation(ctx context.Context, q *db.Queries, chat, rel string, ts time.Time) error {
	rows, err := q.ListSourceChatLocations(ctx, chat)
	if err != nil {
		return err
	}
	for _, row := range rows {
		location := chatLocationFromRow(row)
		if !location.Covers(rel) {
			continue
		}
		if !location.Available() {
			continue
		}
		if location.Hidden {
			return q.SetSourceChatLocationHidden(ctx, db.SetSourceChatLocationHiddenParams{Hidden: 0, ID: location.ID})
		}
		return nil
	}
	anchor, kind := locationAnchor(rel)
	location := ChatLocation{Anchor: anchor, Kind: kind}
	identity, _ := projectroot.OutsideDirectoryIdentity(location.Directory())
	return q.InsertSourceChatLocation(ctx, db.InsertSourceChatLocationParams{
		ID: uuid.NewString(), SessionID: chat, Anchor: anchor, AnchorIdentity: identity, Kind: string(kind), CreatedTs: ts.UTC().Format(time.RFC3339Nano),
	})
}

// ChatLocations lists the locations of sessionID's root chat in anchor order,
// leaving out any location another visible one of the chat covers.
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
	for _, row := range rows {
		all = append(all, chatLocationFromRow(row))
	}
	locations := make([]ChatLocation, 0, len(all))
	for _, location := range all {
		covered := false
		for _, outer := range all {
			if outer.ID != location.ID && !outer.Hidden && outer.Kind != ChatLocationFile && outer.Covers(location.Anchor) {
				covered = true
				break
			}
		}
		if !covered {
			locations = append(locations, location)
		}
	}
	return locations, nil
}

// ChatLocationCovering answers the chat's visible location covering rel.
func (s *Store) ChatLocationCovering(ctx context.Context, sessionID, rel string) (ChatLocation, bool, error) {
	locations, err := s.ChatLocations(ctx, sessionID)
	if err != nil {
		return ChatLocation{}, false, err
	}
	for _, location := range locations {
		if location.Covers(rel) {
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

// HideChatLocation changes whether a location is presented; it revokes and
// deletes nothing.
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

// TrackChatFile tracks an outside file and records the chat's relation to it
// in the same transaction.
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
