package sourceapi

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

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

// Exact-file locations show one entry from their directory.
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
		if !location.Hidden && !pathInsideAttached(p, projectroot.OutsideAbs(location.Anchor)) {
			dto := chatLocationDTO(p, location)
			if s.LocationAccess != nil && dto.Available {
				dto.Access = s.LocationAccess(chatID, p.ID, dto.Path, location.Kind == sourceledger.ChatLocationFile)
			}
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

// An outside write refreshes a directory only if the person opened it.
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
