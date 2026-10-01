package visual

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Cover is a durable project cover reference.
type Cover struct {
	ArtifactID    string
	RootSessionID string
	Source        api.VisualArtifactSource
	UpdatedAt     time.Time
}

// CoverBinding stores at most one cover per project.
type CoverBinding interface {
	GetCover(ctx context.Context, projectID string) (Cover, bool, error)
	SetCover(ctx context.Context, projectID string, cover Cover) error
}

// SessionProjectLookup resolves a session's project.
type SessionProjectLookup func(ctx context.Context, sessionID string) (projectID string, err error)

// DesignateRequest binds an existing VisualArtifact as a project's cover.
type DesignateRequest struct {
	ProjectID     string
	RootSessionID string
	ArtifactID    string
}

// ErrCoverNotFound reports an unavailable cover artifact.
var ErrCoverNotFound = errors.New("cover artifact not found")

// ErrCoverIneligible reports an invalid cover source or project.
var ErrCoverIneligible = errors.New("cover artifact ineligible")

// DesignateCover moves the project's single cover reference.
func DesignateCover(
	ctx context.Context,
	store Store,
	binding CoverBinding,
	lookup SessionProjectLookup,
	req DesignateRequest,
) error {
	if store == nil {
		return fmt.Errorf("visual store not configured")
	}
	if binding == nil {
		return fmt.Errorf("cover binding not configured")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	rootID := strings.TrimSpace(req.RootSessionID)
	artifactID := strings.TrimSpace(req.ArtifactID)
	if projectID == "" || rootID == "" || artifactID == "" {
		return fmt.Errorf("%w: missing ids", ErrCoverNotFound)
	}
	res := store.Resolve(ctx, rootID, artifactID)
	if !res.IsPresent() {
		return fmt.Errorf("%w: %s (%s)", ErrCoverNotFound, artifactID, res.Reason())
	}
	meta := res.Meta()
	if !IsRasterMime(meta.Mime) {
		return fmt.Errorf("%w: mime %s", ErrCoverIneligible, meta.Mime)
	}
	switch meta.Source {
	case api.VisualArtifactSourceCapture, api.VisualArtifactSourceRender:
	default:
		return fmt.Errorf("%w: source %s", ErrCoverIneligible, meta.Source)
	}
	if lookup != nil {
		rootProjectID, lerr := lookup(ctx, rootID)
		if lerr != nil {
			return fmt.Errorf("%w: session lookup failed", ErrCoverIneligible)
		}
		if strings.TrimSpace(rootProjectID) != projectID {
			return fmt.Errorf("%w: project mismatch", ErrCoverIneligible)
		}
	}
	return binding.SetCover(ctx, projectID, Cover{
		ArtifactID:    artifactID,
		RootSessionID: rootID,
		Source:        meta.Source,
		UpdatedAt:     time.Now().UTC(),
	})
}
