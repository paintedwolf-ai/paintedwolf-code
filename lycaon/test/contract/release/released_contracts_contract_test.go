package contract

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/releasecontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The candidate keeps every contract the newest merged stable release fixed,
// and every migration each merged release shipped. A checkout without release
// tags fails rather than passing unchecked.
func TestCandidateKeepsReleasedContracts(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	var report bytes.Buffer
	if err := releasecontract.CheckCompatibility(t.Context(), root, "", &report); err != nil {
		t.Fatalf("release compatibility: %v\n%s", err, &report)
	}
	versions, err := releasecontract.ReleasedVersions(t.Context(), root)
	contractcheck.FailErr(t, "list released versions", err)
	if len(versions) == 0 {
		t.Fatal("no post-v1 release tag is merged into HEAD; fetch tags")
	}
	contractcheck.FailErr(t, "published migrations", releasecontract.CheckPublishedMigrations(t.Context(), root, versions))
}
