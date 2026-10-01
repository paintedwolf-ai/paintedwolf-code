package modelfeed

import (
	"context"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

func getFeedBytes(ctx context.Context, rawURL string) ([]byte, error) {
	return httpclient.GetFeed(ctx, rawURL, httpclient.FeedOptions{
		Class:     egressclass.ModelMetadataRefresh,
		Timeout:   httpclient.CatalogTimeout,
		MaxBytes:  32 << 20,
		UserAgent: "PaintedWolfCode-modelfeed/1.0",
	})
}
