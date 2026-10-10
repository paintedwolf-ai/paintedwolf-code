package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceSearchCursorBindsScopeGenerationAndAddress(t *testing.T) {
	address := project.SourceIndexEntry{RootID: "root", Path: "src/a.go"}
	scope := sourceSearchScope("project", "", "query")
	raw, err := encodeSourceSearchCursor(scope, 42, address)
	testutil.FailErr(t, "encode source cursor", err)
	got, err := decodeSourceSearchCursor(raw, scope, 42)
	testutil.FailErr(t, "decode source cursor", err)
	if got == nil || *got != address {
		t.Fatalf("address=%+v", got)
	}
	if _, err = decodeSourceSearchCursor(raw, scope, 43); !errors.Is(err, pagecursor.ErrExpired) {
		t.Fatalf("generation mismatch=%v, want expired", err)
	}
	if _, err = decodeSourceSearchCursor(raw, sourceSearchScope("project", "", "other"), 42); !errors.Is(err, pagecursor.ErrInvalid) {
		t.Fatalf("other scope=%v, want invalid", err)
	}
}

func declarationQuery(pattern, projectID, root string, match project.DeclarationMatch, hitCap int) project.DeclarationSearchQuery {
	return project.DeclarationSearchQuery{
		ProjectID: projectID,
		Roots:     []project.DeclarationSearchRoot{{ID: "root", Path: root}},
		Pattern:   pattern,
		Match:     match,
		HitCap:    hitCap,
	}
}

func TestDeclarationSearchUsesExtraRowAsLimitProbe(t *testing.T) {
	searchMatches := func(count int) (int, bool, error) {
		t.Helper()
		root := t.TempDir()
		for i := range count {
			name := filepath.Join(root, fmt.Sprintf("match-%02d.go", i))
			testutil.FailErr(t, "write definition match", os.WriteFile(name, []byte("package p\n\nfunc Target() {}\n"), 0o644))
		}
		hits, limited, err := searchDeclarations(context.Background(), declarationQuery("Target", "p1", root, project.DeclarationMatchWholeWord, 3))
		return len(hits), limited.Incomplete(), err
	}

	count, limited, err := searchMatches(3)
	testutil.FailErr(t, "search exact cap", err)
	if limited || count != 3 {
		t.Fatalf("exact cap = (%d, limited %v), want (3, false)", count, limited)
	}

	count, limited, err = searchMatches(4)
	testutil.FailErr(t, "search over cap", err)
	if !limited || count != 3 {
		t.Fatalf("over cap = (%d, limited %v), want (3, true)", count, limited)
	}
}

func TestDeclarationSearchReportsColdCatalogAsPartial(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write definition", os.WriteFile(filepath.Join(root, "target.go"), []byte("func Target() {}\n"), 0o600))
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
		Lane: root, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	testutil.FailErr(t, "hold discovery admission", err)
	defer release()
	hits, partial, err := searchDeclarations(t.Context(), declarationQuery("Target", "cold-definition", root, project.DeclarationMatchWholeWord, 3))
	testutil.FailErr(t, "search cold definition", err)
	if len(hits) != 0 || !partial.Incomplete() || len(partial.Gaps) != 1 || partial.Gaps[0].Reason != project.DeclarationCatalogWarming || partial.Gaps[0].Count != 1 {
		t.Fatalf("cold definition hits=%d partial=%v", len(hits), partial)
	}
}

func TestDeclarationSearchMatchModes(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "a.go"), []byte(
		"package a\n\nfunc ParseConfig() {}\n\nfunc parser() {}\n\nvar x = \".+\"\n"), 0o644))
	lines := func(pattern string, match project.DeclarationMatch) []string {
		t.Helper()
		hits, _, err := searchDeclarations(context.Background(), declarationQuery(pattern, "modes", root, match, 10))
		testutil.FailErr(t, "search "+pattern, err)
		out := make([]string, 0, len(hits))
		for _, hit := range hits {
			if hit.RootID != "root" || hit.Path != "a.go" {
				t.Fatalf("hit = %+v, want root-addressed a.go", hit)
			}
			out = append(out, hit.Snippet)
		}
		return out
	}
	if got := lines("parse", project.DeclarationMatchSubstring); len(got) != 2 {
		t.Fatalf("substring hits = %q, want both parse lines ignoring case", got)
	}
	if got := lines("parse", project.DeclarationMatchWholeWord); len(got) != 0 {
		t.Fatalf("whole-word hits = %q, want none", got)
	}
	if got := lines(".+", project.DeclarationMatchSubstring); len(got) != 1 {
		t.Fatalf("literal hits = %q, want the one quoted line", got)
	}
	if got := lines(`P\w*C`, project.DeclarationMatchRegexp); len(got) != 1 {
		t.Fatalf("case-sensitive expression hits = %q, want ParseConfig", got)
	}
}
