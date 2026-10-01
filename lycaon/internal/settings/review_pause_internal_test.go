package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The time-boxed review skip rests on two facts: a paused rule is inert only
// while disabled_until is in the future, and the pause survives the YAML
// round-trip through the project overlay. A silently dropped field would turn
// "Skip review for 1 day" into a permanent skip-nothing.

func TestMatchesReviewPathHonorsDisabledUntil(t *testing.T) {
	future := time.Now().Add(30 * time.Minute)
	past := time.Now().Add(-30 * time.Minute)
	cases := []struct {
		name string
		rule ContentReviewRule
		want bool
	}{
		{"active rule matches", ContentReviewRule{Path: "src/**"}, true},
		{"paused rule is inert", ContentReviewRule{Path: "src/**", DisabledUntil: &future}, false},
		{"expired pause asks again", ContentReviewRule{Path: "src/**", DisabledUntil: &past}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ReviewConfig{ReviewPaths: []ContentReviewRule{tc.rule}}
			if got := cfg.MatchesReviewPath("write", "src/foo.go"); got != tc.want {
				t.Fatalf("MatchesReviewPath = %v want %v", got, tc.want)
			}
		})
	}
}

func TestReviewPauseSurvivesProjectRoundTrip(t *testing.T) {
	// No bundled review rules, so the single rule read back below can only have
	// come from the project overlay's YAML round-trip.
	configtest.Overlay(t, map[config.Rel]string{config.Review: "review_paths: []\n"})
	tmp := t.TempDir()
	store, err := NewReviewStoreAt(filepath.Join(tmp, "global-review.yaml"))
	testutil.FailErr(t, "NewReviewStoreAt", err)
	projectDir := t.TempDir()
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	testutil.FailErr(t, "PutProject", store.PutProject(projectDir, ReviewConfig{
		ReviewPaths: []ContentReviewRule{{Path: "src/**", DisabledUntil: &until}},
	}))

	// A fresh store reads the persisted overlay from disk — no warm cache.
	reread, err := NewReviewStoreAt(filepath.Join(tmp, "global-review.yaml"))
	testutil.FailErr(t, "reopen review store", err)
	cfg := reread.Get("project", projectDir)
	if len(cfg.ReviewPaths) != 1 {
		t.Fatalf("rules = %+v", cfg.ReviewPaths)
	}
	got := cfg.ReviewPaths[0].DisabledUntil
	if got == nil || !got.Equal(until) {
		t.Fatalf("disabled_until = %v want %v", got, until)
	}
	if cfg.MatchesReviewPath("write", "src/foo.go") {
		t.Fatal("persisted pause did not suppress review")
	}
}
