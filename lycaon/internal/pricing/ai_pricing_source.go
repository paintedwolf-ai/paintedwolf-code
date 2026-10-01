package pricing

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

const (
	aiPricingPageSize = 100
	maxAIPricingPages = 1000
)

type aiPricingSource struct {
	cfg      SourceConfig
	fetch    func(context.Context, string) ([]byte, error)
	now      func() time.Time
	maxBytes int64
}

func (s *aiPricingSource) ID() string    { return s.cfg.ID }
func (s *aiPricingSource) Kind() string  { return s.cfg.Kind }
func (s *aiPricingSource) Label() string { return s.cfg.Label }
func (s *aiPricingSource) URL() string   { return s.cfg.URL }

func (s *aiPricingSource) Fetch(ctx context.Context) (RateTable, error) {
	bodies := make([][]byte, 0, 8)
	var totalBytes int64
	offset := 0
	for pageNumber := 0; pageNumber < maxAIPricingPages; pageNumber++ {
		pageURL, err := aiPricingURL(s.cfg.URL, offset)
		if err != nil {
			return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		body, err := s.fetch(ctx, pageURL)
		if err != nil {
			return RateTable{}, err
		}
		totalBytes += int64(len(body))
		if totalBytes > s.maxBytes {
			return RateTable{}, fmt.Errorf("%w: paginated payload exceeds %d bytes", ErrInvalid, s.maxBytes)
		}
		page, err := decodeAIPricingPage(body)
		if err != nil {
			return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		bodies = append(bodies, body)
		if len(page.Data) < aiPricingPageSize {
			table, err := parseAIPricingPages(bodies)
			if err != nil {
				return RateTable{}, fmt.Errorf("%w: %w", ErrInvalid, err)
			}
			table.FetchedAt = s.now()
			return table, nil
		}
		offset += len(page.Data)
	}
	return RateTable{}, fmt.Errorf("%w: pagination exceeds %d pages", ErrInvalid, maxAIPricingPages)
}

func aiPricingURL(rawURL string, offset int) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("ai-pricing-fyi: invalid url: %w", err)
	}
	query := u.Query()
	query.Set("limit", strconv.Itoa(aiPricingPageSize))
	query.Set("offset", strconv.Itoa(offset))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func newAIPricingSource(
	cfg SourceConfig,
	fetch func(context.Context, string) ([]byte, error),
	now func() time.Time,
	maxBytes int64,
) *aiPricingSource {
	if now == nil {
		now = time.Now
	}
	if maxBytes <= 0 {
		maxBytes = MaxPricingPayloadBytes
	}
	return &aiPricingSource{cfg: cfg, fetch: fetch, now: now, maxBytes: maxBytes}
}
