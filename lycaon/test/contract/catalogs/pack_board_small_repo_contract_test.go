package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

// The tier cutoffs are pinned at the file count that changes the rendered line.
func TestPackBoardSmallRepoTierBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		count int
		want  []string
		omit  []string
	}{
		{"last tiny count lists files", packboard.TinyMaxFiles, []string{"Files:"}, []string{"Top-level:"}},
		{"first small count lists top level", packboard.TinyMaxFiles + 1, []string{"Top-level:"}, []string{"Files:"}},
		{"last medium count still renders", packboard.MediumMaxFiles, []string{"Top-level:"}, []string{"Files:"}},
		{"past medium renders no layout", packboard.MediumMaxFiles + 1, nil, []string{"Files:", "Top-level:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := api.RepoBrief{
				FileCount: tc.count,
				Languages: []string{"Go"},
				Layout: api.RepoLayout{
					Files:    []string{"a.go", "b.go"},
					TopLevel: []string{"cmd/", "internal/", "go.mod"},
				},
			}
			joined := strings.Join(
				packboard.BuildOrientationLines(api.BoardSnapshot{Repo: repo}, packboard.OrientOpts{}), "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("%d files: lines = %q missing %q", tc.count, joined, want)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(joined, omit) {
					t.Fatalf("%d files: lines = %q must not contain %q", tc.count, joined, omit)
				}
			}
		})
	}
}

func TestPackBoardSmallRepoLayoutRenderer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		repo api.RepoBrief
		want []string
		omit []string
	}{
		{
			name: "tiny files line",
			repo: api.RepoBrief{
				FileCount: 2,
				Languages: []string{"HTML"},
				Layout:    api.RepoLayout{Files: []string{"a.html", "b.md"}},
			},
			want: []string{"Repo: 2 files · HTML", "Files: a.html, b.md"},
		},
		{
			name: "small top-level line",
			repo: api.RepoBrief{
				FileCount: 12,
				Languages: []string{"Go"},
				Layout:    api.RepoLayout{TopLevel: []string{"cmd/", "internal/", "README.md", "go.mod"}},
			},
			want: []string{"Repo: 12 files · Go", "Top-level:"},
		},
		{
			name: "large repo no layout",
			repo: api.RepoBrief{
				FileCount: 600,
				Languages: []string{"Go"},
			},
			want: []string{"Repo: 600 files · Go"},
			omit: []string{"Files:", "Top-level:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := packboard.BuildOrientationLines(api.BoardSnapshot{Repo: tc.repo}, packboard.OrientOpts{})
			joined := strings.Join(lines, "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("lines = %q missing %q", joined, want)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(joined, omit) {
					t.Fatalf("lines = %q must not contain %q", joined, omit)
				}
			}
		})
	}
}
