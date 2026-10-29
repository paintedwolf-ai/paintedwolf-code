package native

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

// PublicationBarrier orders a program's native writes before the external
// processes that depend on them. External programs read published disk, so a
// write accepted into a shared document but not saved blocks the launch; the
// accepted state stays recoverable and no other draft is ever saved for it.
type PublicationBarrier struct {
	mu      sync.Mutex
	pending []pendingPublication
}

type pendingPublication struct {
	path       string
	documentID string
	revision   int64
}

// PublicationPending lists the accepted writes whose files do not hold them.
type PublicationPending struct {
	Paths []string
}

func (p *PublicationPending) Error() string {
	return "accepted program writes are not published: " + strings.Join(p.Paths, ", ")
}

func (p *PublicationPending) Unwrap() error {
	return &toolrejection.ToolReject{Code: "COMMAND_PUBLICATION_PENDING", Data: map[string]any{"path": strings.Join(p.Paths, ", ")}}
}

// Track records one pinned write's outcome. A saved write, or one that wrote
// the file directly, is already published.
func (b *PublicationBarrier) Track(result WriteResult) error {
	if !result.Accepted || result.Saved {
		return nil
	}
	if result.DocumentID == "" || result.AcceptedRevision <= 0 {
		return fmt.Errorf("unpublished write %s has no accepted document revision", result.Path)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = append(b.pending, pendingPublication{path: result.Path, documentID: result.DocumentID, revision: result.AcceptedRevision})
	return nil
}

// AdmitDependentLaunch returns nil once every tracked write is published, by
// this program or a later save of the same document; otherwise it refuses the
// launch with PublicationPending. It never saves, retries or waits on a draft.
func (b *PublicationBarrier) AdmitDependentLaunch(ctx context.Context, tctx tools.ToolContext) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	documents, ok := sourceview.ProjectDocuments(tctx)
	if !ok {
		return &PublicationPending{Paths: pendingPaths(b.pending)}
	}
	remaining := make([]pendingPublication, 0, len(b.pending))
	for _, write := range b.pending {
		published, err := documents.PublishedThrough(ctx, tctx.Identity.ProjectID, write.documentID, write.revision)
		if err != nil {
			return err
		}
		if !published {
			remaining = append(remaining, write)
		}
	}
	b.pending = remaining
	if len(remaining) > 0 {
		return &PublicationPending{Paths: pendingPaths(remaining)}
	}
	return nil
}

func pendingPaths(pending []pendingPublication) []string {
	paths := make([]string, len(pending))
	for i, write := range pending {
		paths[i] = write.path
	}
	return paths
}
