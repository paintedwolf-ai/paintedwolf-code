package sourceapi

import (
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWatchCoverageDistinguishesLazyPolicyFromTruncation(t *testing.T) {
	lazy := SourceWatchCoverageDTO(repochange.WatchCoverage{Watching: true, Recursive: true, PolicyUnwatched: 3})
	if lazy.State != wire.SourceWatchLive || lazy.UnwatchedDirectories != 0 || lazy.PolicyUnwatched != 3 {
		t.Fatalf("lazy policy reported watcher failure: %+v", lazy)
	}
	partial := SourceWatchCoverageDTO(repochange.WatchCoverage{Watching: true, Truncated: 2, PolicyUnwatched: 3})
	if partial.State != wire.SourceWatchPartial || partial.UnwatchedDirectories != 2 || partial.PolicyUnwatched != 3 {
		t.Fatalf("partial watcher lost policy distinction: %+v", partial)
	}
}
