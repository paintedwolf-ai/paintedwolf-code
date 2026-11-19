package providers

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFixtureModelFeedRefreshesFromItsDocument(t *testing.T) {
	document := filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "models-dev", "api.json")
	feed, err := fixtureModelFeed(document, t.TempDir())
	testutil.FailErr(t, "fixture feed", err)
	doc, err := feed.Refresh(t.Context())
	testutil.FailErr(t, "refresh", err)
	if len(doc.Providers) == 0 {
		t.Fatal("the refreshed document holds no providers")
	}

	missing, err := fixtureModelFeed(filepath.Join(t.TempDir(), "absent.json"), t.TempDir())
	testutil.FailErr(t, "fixture feed", err)
	if _, err := missing.Refresh(t.Context()); err == nil {
		t.Fatal("a missing document refreshed")
	}
}
