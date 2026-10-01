package pricing

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

func pricingFetch(opts RegistryOptions) func(context.Context, string) ([]byte, error) {
	getBytes := opts.GetBytes
	if getBytes == nil {
		getBytes = func(ctx context.Context, rawURL string) ([]byte, error) {
			return httpclient.GetFeed(ctx, rawURL, httpclient.FeedOptions{
				Class:     egressclass.PricingFeedRefresh,
				Timeout:   opts.Timeout,
				MaxBytes:  bytebound.Transport(opts.MaxBytes),
				UserAgent: "PaintedWolfCode-pricing/1.0",
			})
		}
	}
	return func(ctx context.Context, rawURL string) ([]byte, error) {
		body, err := getBytes(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		if errors.Is(err, httpclient.ErrResponseBodyTooLarge) {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
}
