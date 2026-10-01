package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/catalogruntime"
)

const (
	kindModelsDev    = "models-dev"
	kindLitellm      = "litellm"
	kindAIPricingFYI = "ai-pricing-fyi"
)

// Source is one pricing feed driver.
type Source interface {
	ID() string
	Kind() string
	Label() string
	URL() string
	Fetch(ctx context.Context) (RateTable, error)
}

// Parser turns feed bytes into a RateTable (no network).
type Parser func(body []byte) (RateTable, error)

var parsers = map[string]Parser{
	kindLitellm: ParseLitellm,
}

// ParserForKind returns the Parse function for a driver kind.
func ParserForKind(kind string) (Parser, error) {
	p, ok := parsers[kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	return p, nil
}

type feedSource struct {
	cfg   SourceConfig
	parse Parser
	fetch func(ctx context.Context, rawURL string) ([]byte, error)
	now   func() time.Time
}

func (s *feedSource) ID() string    { return s.cfg.ID }
func (s *feedSource) Kind() string  { return s.cfg.Kind }
func (s *feedSource) Label() string { return s.cfg.Label }
func (s *feedSource) URL() string   { return s.cfg.URL }

func (s *feedSource) Fetch(ctx context.Context) (RateTable, error) {
	body, err := s.fetch(ctx, s.cfg.URL)
	if err != nil {
		return RateTable{}, err
	}
	table, err := s.parse(body)
	if err != nil {
		return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := validateRateTable(table); err != nil {
		return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	table.FetchedAt = s.now()
	if table.Status == "" {
		table.Status = StatusOK
	}
	return table, nil
}

func newFeedSource(cfg SourceConfig, fetch func(context.Context, string) ([]byte, error), now func() time.Time) (*feedSource, error) {
	if cfg.Kind == kindModelsDev {
		return nil, fmt.Errorf("%w: models-dev is modelfeed projection, not an HTTP parser", ErrUnknownKind)
	}
	parse, err := ParserForKind(cfg.Kind)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &feedSource{cfg: cfg, parse: parse, fetch: fetch, now: now}, nil
}

type sourceBuild struct {
	config  SourceConfig
	options RegistryOptions
	fetch   func(context.Context, string) ([]byte, error)
}

// sourceFactories builds the pricing adapter selected by the catalog.
var sourceFactories = catalogruntime.NewFactorySet(
	map[string]catalogruntime.Factory[sourceBuild, Source]{
		kindModelsDev: func(_ context.Context, build sourceBuild) (Source, error) {
			return newModelsDevSource(build.config, build.options.ModelFeed, build.options.Now), nil
		},
		kindAIPricingFYI: func(_ context.Context, build sourceBuild) (Source, error) {
			return newAIPricingSource(
				build.config, build.fetch, build.options.Now, build.options.MaxBytes,
			), nil
		},
		kindLitellm: func(_ context.Context, build sourceBuild) (Source, error) {
			return newFeedSource(build.config, build.fetch, build.options.Now)
		},
	},
	nil,
)
