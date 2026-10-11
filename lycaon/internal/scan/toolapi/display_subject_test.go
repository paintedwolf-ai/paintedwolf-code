package toolapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type subjectScans struct {
	scanbase.ScanCoordinator
	records map[string]*api.CodeScan
	reads   int
}

func (s *subjectScans) Summary(_ context.Context, id string) (*api.CodeScan, error) {
	s.reads++
	return s.records[id], nil
}

func TestScanSubjectsKeepScopeAndMultiplicity(t *testing.T) {
	root := t.TempDir()
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "resolve scan root", err)
	at := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	coord := &subjectScans{records: map[string]*api.CodeScan{
		"one":   {CanonicalPath: canonical, Categories: []api.ScanCategory{api.ScanCategorySecurity}, StartedAt: &at},
		"two":   {CanonicalPath: canonical, Categories: []api.ScanCategory{api.ScanCategorySecret}, StartedAt: &at},
		"other": {CanonicalPath: t.TempDir(), Categories: []api.ScanCategory{api.ScanCategorySecurity}},
	}}
	tc := tools.ToolContext{
		Source:  tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}},
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}
	label := scanDisplaySubject(t.Context(), tc, coord, []string{"one", "two"})
	if !strings.HasPrefix(label, "2 scans · ") || !strings.Contains(label, "security") || !strings.Contains(label, "secret") || !strings.Contains(label, "2026-09-30") {
		t.Fatalf("scan subject = %q", label)
	}
	for _, id := range []string{"missing", "other"} {
		if got := scanDisplaySubject(t.Context(), tc, coord, []string{id}); got != "" {
			t.Fatalf("unavailable target %s leaked %q", id, got)
		}
	}
	coord.reads = 0
	tc.Effects.Out = nil
	if label := scanDisplaySubject(t.Context(), tc, coord, []string{"one"}); label != "" || coord.reads != 0 {
		t.Fatal("non-presenting caller performed display lookup")
	}
}
