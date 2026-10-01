package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPricingFetchMapsTypedFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  error
	}{
		{"oversized sentinel", httpclient.ErrResponseBodyTooLarge, ErrInvalid},
		{"oversized", &httpclient.ResponseBodyTooLargeError{Limit: 8}, ErrInvalid},
		{"wrapped oversized", fmt.Errorf("fetch: %w", &httpclient.ResponseBodyTooLargeError{Limit: 8}), ErrInvalid},
		{"status", &httpclient.FeedStatusError{StatusCode: 503}, ErrUnreachable},
		{"destination", &egress.DestinationDeniedError{Reason: "private address"}, ErrUnreachable},
		{"timeout", context.DeadlineExceeded, ErrUnreachable},
		{"canceled", context.Canceled, ErrUnreachable},
		{"read", errors.New("read failed"), ErrUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetch := pricingFetch(RegistryOptions{GetBytes: func(context.Context, string) ([]byte, error) { return []byte("partial"), tc.cause }})
			body, err := fetch(t.Context(), "https://8.8.8.8/feed")
			if body != nil || !errors.Is(err, tc.want) || !errors.Is(err, tc.cause) {
				t.Fatalf("body=%q err=%v", body, err)
			}
		})
	}
}

func TestRegistryHonorsConfiguredFetchTimeout(t *testing.T) {
	reg, err := NewRegistryFromConfig(t.Context(), SourcesConfig{Sources: []SourceConfig{{
		ID: "litellm", Kind: kindLitellm, Label: "LiteLLM", URL: "https://8.8.8.8/feed",
	}}}, RegistryOptions{CacheDir: t.TempDir(), Timeout: time.Nanosecond})
	testutil.FailErr(t, "create registry", err)
	_, err = reg.Refresh(t.Context(), "litellm")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("configured deadline not applied: %v", err)
	}
}

func TestPaginatedPricingKeepsAggregateByteLimit(t *testing.T) {
	rows := make([]aiPricingRow, aiPricingPageSize)
	for i := range rows {
		rows[i] = aiPricingRow{ProviderSlug: "openai", ModelName: fmt.Sprintf("model-%d", i), Metric: "input_token", Unit: "per_1m_tokens", PriceNumeric: 2, Currency: "USD"}
	}
	first, err := json.Marshal(aiPricingPage{Data: rows})
	testutil.FailErr(t, "marshal full page", err)
	last, err := json.Marshal(aiPricingPage{Data: rows[:1]})
	testutil.FailErr(t, "marshal final page", err)
	for _, exceeds := range []bool{false, true} {
		t.Run(strconv.FormatBool(exceeds), func(t *testing.T) {
			limit := int64(len(first) + len(last))
			if exceeds {
				limit--
			}
			fetched := &seqFetcher{bodies: [][]byte{first, last}}
			src := newAIPricingSource(SourceConfig{URL: "https://feed.example/prices"}, fetched.Get, time.Now, limit)
			table, err := src.Fetch(t.Context())
			if exceeds {
				if !errors.Is(err, ErrInvalid) || len(table.Rates) != 0 {
					t.Fatalf("overflow produced rates: %d, %v", len(table.Rates), err)
				}
			} else {
				testutil.FailErr(t, "fetch at aggregate limit", err)
				if len(table.Rates) != aiPricingPageSize {
					t.Fatalf("rates=%d", len(table.Rates))
				}
			}
			if len(fetched.urls) != 2 {
				t.Fatalf("pages=%d", len(fetched.urls))
			}
		})
	}
}
