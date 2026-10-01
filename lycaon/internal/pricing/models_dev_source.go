package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/modelfeed"
)

// ModelFeed supplies shared model metadata.
type ModelFeed interface {
	Document(ctx context.Context) (*modelfeed.Document, error)
	Refresh(ctx context.Context) (*modelfeed.Document, error)
	Snapshot() (doc *modelfeed.Document, status string, usable bool)
}

// modelsDevSource projects shared model metadata into rates.
type modelsDevSource struct {
	cfg  SourceConfig
	feed ModelFeed
	now  func() time.Time
}

func (s *modelsDevSource) ID() string    { return s.cfg.ID }
func (s *modelsDevSource) Kind() string  { return s.cfg.Kind }
func (s *modelsDevSource) Label() string { return s.cfg.Label }
func (s *modelsDevSource) URL() string   { return "" }

func (s *modelsDevSource) Fetch(ctx context.Context) (RateTable, error) {
	return s.project(ctx, false)
}

// ForceRefresh refreshes metadata before projecting rates.
func (s *modelsDevSource) ForceRefresh(ctx context.Context) (RateTable, error) {
	return s.project(ctx, true)
}

func (s *modelsDevSource) project(ctx context.Context, force bool) (RateTable, error) {
	if s.feed == nil {
		return RateTable{}, fmt.Errorf("%w: models-dev requires shared modelfeed", ErrUnreachable)
	}
	var doc *modelfeed.Document
	var err error
	if force {
		doc, err = s.feed.Refresh(ctx)
		if err != nil {
			return RateTable{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
		}
	} else {
		doc, err = s.feed.Document(ctx)
		if err != nil || doc == nil {
			if snap, _, usable := s.feed.Snapshot(); usable {
				doc = snap
			} else {
				if err == nil {
					err = fmt.Errorf("models-dev document unavailable")
				}
				return RateTable{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
			}
		}
	}
	if doc == nil {
		return RateTable{}, fmt.Errorf("%w: models-dev document unavailable", ErrUnreachable)
	}
	table := RateTableFromDocument(doc)
	if err := validateRateTable(table); err != nil {
		return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if !doc.FetchedAt.IsZero() {
		table.FetchedAt = doc.FetchedAt
	} else {
		table.FetchedAt = s.now()
	}
	table.Status = StatusOK
	return table, nil
}

func newModelsDevSource(cfg SourceConfig, feed ModelFeed, now func() time.Time) *modelsDevSource {
	if now == nil {
		now = time.Now
	}
	return &modelsDevSource{cfg: cfg, feed: feed, now: now}
}
