package blueprint

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// Store persists blueprint documents as convention files.
type Store interface {
	Create(ctx context.Context, bp *api.Blueprint) error
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
	// UpdateContent replaces the document at path. expectDigest is ContentDigest
	// of the bytes it was computed from; any other destination bytes are refused.
	UpdateContent(ctx context.Context, projectID, path, content, expectDigest string) (*api.Blueprint, error)
	Delete(ctx context.Context, projectID, path string) error
	// Rename moves a convention file. Destination must be free.
	Rename(ctx context.Context, projectID, from, to string) error
	// FreePath returns a unique convention path for title, treating except as free.
	FreePath(ctx context.Context, projectID, title, except string) (string, error)
	List(ctx context.Context, projectID string) ([]api.BlueprintSummary, error)
}
