package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// OutsideReadBinding uses the open document's parent or a chat's recorded anchor.
func (s *Store) OutsideReadBinding(ctx context.Context, projectID, fileID, rel string) (projectroot.OutsideBinding, error) {
	parent := path.Dir(rel)
	binding, err := s.queries.GetEditorOutsideParentBinding(ctx, db.GetEditorOutsideParentBindingParams{ProjectID: projectID, FileID: fileID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return projectroot.OutsideBinding{}, err
	}
	if dir, identity, ok := strings.Cut(binding, "\x00"); ok && dir == parent && identity != "" {
		return projectroot.OutsideBinding{Directory: filepath.Clean(projectroot.OutsideAbs(dir)), Identity: identity}, nil
	}
	exact := confine.ClassifyBlockedWrite(projectroot.OutsideAbs(rel)).Kind != confine.WriteSubjectOrdinary
	for anchor := rel; anchor != "."; anchor = path.Dir(anchor) {
		identity, err := s.queries.FindSourceFileChatAnchor(ctx, db.FindSourceFileChatAnchorParams{FileID: fileID, Anchor: anchor, Path: rel})
		switch {
		case err == nil:
			dir := anchor
			if anchor == rel {
				dir = parent
			}
			return projectroot.OutsideBinding{Directory: filepath.Clean(projectroot.OutsideAbs(dir)), Identity: identity}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return projectroot.OutsideBinding{}, err
		}
		if exact {
			break
		}
	}
	return projectroot.OutsideBinding{}, ErrHistoryNotFound
}
