package modelfeed

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestModelFeedUsesPublicHTTPSBoundary(t *testing.T) {
	for _, rawURL := range []string{"http://8.8.8.8/feed", "https://127.0.0.1/feed", "https://169.254.169.254/feed"} {
		if _, err := getFeedBytes(t.Context(), rawURL); err == nil {
			t.Fatalf("accepted %s", rawURL)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := getFeedBytes(ctx, DefaultURL); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFailedRefreshPreservesModelFeed(t *testing.T) {
	for _, failure := range []error{
		&httpclient.ResponseBodyTooLargeError{Limit: 32 << 20},
		&httpclient.FeedStatusError{StatusCode: 503},
		&egress.DestinationDeniedError{Reason: "private address"},
		context.DeadlineExceeded,
		errors.New("interrupted transfer"),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			var fetchErr error
			feed, err := New(Options{CacheDir: t.TempDir(), TTL: time.Hour, GetBytes: func(context.Context, string) ([]byte, error) {
				return []byte(`{"provider":{"models":{"model":{}}}}`), fetchErr
			}})
			testutil.FailErr(t, "create feed", err)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			feed.now = func() time.Time { return now }
			publications := 0
			feed.AddRefreshListener(func() { publications++ })
			good, err := feed.Refresh(t.Context())
			testutil.FailErr(t, "seed good feed", err)
			beforeJSON, err := os.ReadFile(feed.cacheJSONPath())
			testutil.FailErr(t, "read JSON cache", err)
			beforeMeta, err := os.ReadFile(feed.cacheMetaPath())
			testutil.FailErr(t, "read metadata cache", err)
			fetchErr = failure
			now = now.Add(2 * time.Hour)
			_, err = feed.Refresh(t.Context())
			if !errors.Is(err, failure) {
				t.Fatalf("refresh failure lost: %v", err)
			}
			cached, err := feed.Document(t.Context())
			testutil.FailErr(t, "read stale document", err)
			if cached != good || feed.FetchedAt() != good.FetchedAt || feed.Status() != StatusStale || publications != 1 {
				t.Fatalf("failed refresh published or replaced cache: status=%s publications=%d", feed.Status(), publications)
			}
			afterJSON, err := os.ReadFile(feed.cacheJSONPath())
			testutil.FailErr(t, "reread JSON cache", err)
			afterMeta, err := os.ReadFile(feed.cacheMetaPath())
			testutil.FailErr(t, "reread metadata cache", err)
			if !bytes.Equal(beforeJSON, afterJSON) || !bytes.Equal(beforeMeta, afterMeta) {
				t.Fatal("failed refresh replaced disk cache")
			}
		})
	}
}
