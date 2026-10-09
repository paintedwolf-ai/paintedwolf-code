package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type currentSourceSnapshot struct {
	document *sourcecomparison.CurrentDocument
	stream   *project.SourceStream
	users    atomic.Int64
}

func (snapshot *currentSourceSnapshot) retain() { snapshot.users.Add(1) }
func (snapshot *currentSourceSnapshot) close() {
	if snapshot.users.Add(-1) == 0 {
		snapshot.document.Close()
		snapshot.stream.Close()
	}
}

func currentSourceChanged() error {
	return &comparisonFailure{wire.ApiErrorCodeSourceVersionChanged, "The file changed. Reload it to read the current version."}
}

func (s *ComparisonViews) prepareCurrentSource(view *sourceView, p *project.Project) error {
	source := view.comparisonData.comparisonSource.Current
	stream, err := project.OpenProjectSourceStream(p, project.SourceReadRequest{Path: source.Path, RootID: source.RootID, DecodeAs: source.DecodeAs})
	if err != nil {
		return currentSourceReadError(err)
	}
	document, err := sourcecomparison.NewCurrent(view.ctx, stream.File, stream.Encoding, stream.Path, view.comparisonData.comparisonBudget, s.sourceViews.snapshotDisk)
	if err != nil {
		stream.Close()
		return currentSourceReadError(err)
	}
	if !stream.Current() {
		document.Close()
		stream.Close()
		return currentSourceChanged()
	}
	snapshot := &currentSourceSnapshot{document: document, stream: stream}
	snapshot.users.Store(1)
	s.installCurrentSource(view, snapshot)
	s.background.Go(view.ctx, func(ctx context.Context) { s.Watch.ensureWorkspaceWatch(ctx, p) })
	return nil
}

func (s *ComparisonViews) installCurrentSource(view *sourceView, snapshot *currentSourceSnapshot) {
	projection := &sourceViewProjection{comparisonProjection: snapshot.document, closeSource: snapshot.close}
	projection.users.Store(1)
	endpoint := &wire.SourceReaderEndpoint{RootID: snapshot.stream.RootID, Path: snapshot.stream.Path,
		State: "content", Availability: "available", Sha256: snapshot.document.RawSHA256, SizeBytes: snapshot.stream.SizeBytes,
		SecretScreen: &wire.SecretScreen{Truncated: true}}
	view.mu.Lock()
	view.comparisonData.current, view.comparisonData.projection = snapshot, projection
	view.comparisonData.details = &wire.SourceComparisonDetails{InRange: true, Before: endpoint, After: endpoint, Summary: &snapshot.document.Summary}
	view.state, view.projectionRevision = "ready", uuid.NewString()
	view.mu.Unlock()
	stop := sourcefeed.Subscribe(view.scope.Project, view.workspaceID, func(sourcefeed.Notice) {
		if !snapshot.stream.Current() {
			view.fail(currentSourceChanged())
			view.notifier.Notify(true)
		}
	})
	context.AfterFunc(view.ctx, stop)
}

func currentSourceReadError(err error) error {
	var encoding *project.SourceUnsupportedEncodingError
	switch {
	case errors.As(err, &encoding):
		return &comparisonFailure{wire.ApiErrorCodeUnsupportedEncoding, fmt.Sprintf("This file uses an unsupported encoding (%s). Choose a supported encoding to read it.", encoding.Detected)}
	case errors.Is(err, textfile.ErrUnsupported):
		return &comparisonFailure{wire.ApiErrorCodeUnsupportedEncoding, "The file contains invalid text for the selected encoding."}
	case errors.Is(err, project.ErrSourceBinary), errors.Is(err, textfile.ErrBinary):
		return &comparisonFailure{wire.ApiErrorCodeSourceBinary, "This file contains binary data and cannot be shown as text."}
	case errors.Is(err, os.ErrNotExist), errors.Is(err, project.ErrSourceNotFound):
		return &comparisonFailure{wire.ApiErrorCodeSourceNotFound, "This file is no longer available."}
	default:
		return err
	}
}
