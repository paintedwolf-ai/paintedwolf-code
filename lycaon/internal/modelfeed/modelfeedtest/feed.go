// Package modelfeedtest publishes model feed fixtures through the runtime cache.
package modelfeedtest

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Publish refreshes a feed from fixture bytes with no network access.
func Publish(t testing.TB, raw []byte) *modelfeed.Feed {
	t.Helper()
	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: t.TempDir(),
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
	})
	testutil.FailErr(t, "open fixture model feed", err)
	_, err = feed.Refresh(t.Context())
	testutil.FailErr(t, "publish fixture model feed", err)
	return feed
}

// Document returns the live document from a published fixture.
func Document(t testing.TB, raw []byte) *modelfeed.Document {
	t.Helper()
	doc, _, usable := Publish(t, raw).Snapshot()
	if !usable {
		t.Fatal("fixture contains no usable model feed")
	}
	return doc
}
