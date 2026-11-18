package packboard

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// The tier cutoffs choose which layout list renders and under which budget.
func TestLayoutTierBoundariesSelectTierInputs(t *testing.T) {
	files := []string{"tier-file-a", "tier-file-b"}
	topLevel := []string{"tier-top-a/", "tier-top-b/"}
	cases := []struct {
		count  int
		names  []string
		budget int
	}{
		{0, nil, 0},
		{1, files, MaxLayoutLineBytes},
		{TinyMaxFiles, files, MaxLayoutLineBytes},
		{TinyMaxFiles + 1, topLevel, MaxLayoutLineBytes},
		{SmallMaxFiles, topLevel, MaxLayoutLineBytes},
		{SmallMaxFiles + 1, topLevel, MaxMediumLayoutLineBytes},
		{MediumMaxFiles, topLevel, MaxMediumLayoutLineBytes},
		{MediumMaxFiles + 1, nil, 0},
	}
	for _, tc := range cases {
		repo := api.RepoBrief{FileCount: tc.count, Layout: api.RepoLayout{Files: files, TopLevel: topLevel}}
		names, _, budget, perNameCap := layoutLineInputs(repo)
		if !slices.Equal(names, tc.names) || budget != tc.budget || perNameCap != tc.budget/3 {
			t.Errorf("%d files: names=%v budget=%d cap=%d, want names=%v budget=%d cap=%d",
				tc.count, names, budget, perNameCap, tc.names, tc.budget, tc.budget/3)
		}
	}
}
