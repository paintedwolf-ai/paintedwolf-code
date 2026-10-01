package board

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/board/factscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// InjectBuilder adapts SnapshotBuilder for coordinator board inject.
type InjectBuilder struct {
	*SnapshotBuilder
	Projects project.Registry
}

// WithRepositoryFacts scopes repository reads to one prompt preparation.
func (b *InjectBuilder) WithRepositoryFacts(ctx context.Context) context.Context {
	return factscope.WithScope(ctx)
}

// BuildBoardSnapshot implements assembly.BoardSnapshotBuilder.
func (b *InjectBuilder) BuildBoardSnapshot(ctx context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel, roots []projectroot.RootRef) (*api.BoardSnapshot, error) {
	if b == nil || b.SnapshotBuilder == nil {
		return nil, nil
	}
	if roots == nil && b.Projects != nil && strings.TrimSpace(projectID) != "" {
		if p, err := b.Projects.Get(ctx, projectID); err == nil && p != nil {
			roots = project.RootRefsFrom(p)
		}
	}
	return b.Build(ctx, projectID, workspacePath, sessionID, level, roots)
}

// InjectFormatter adapts CompactFormatter for coordinator board inject.
type InjectFormatter struct {
	*CompactFormatter
}

// FormatBoardInject implements assembly.BoardPackFormatter.
func (f *InjectFormatter) FormatBoardInject(snap api.BoardSnapshot, omitDelegation bool, now time.Time) (string, bool) {
	if f == nil || f.CompactFormatter == nil {
		return "", false
	}
	body, _, _ := f.FormatInject(snap, omitDelegation, now)
	body = strings.TrimSpace(body)
	return body, body != ""
}

// DefaultInjectFormatter returns a formatter adapter for wiring.
func DefaultInjectFormatter() *InjectFormatter {
	return &InjectFormatter{CompactFormatter: &CompactFormatter{}}
}
