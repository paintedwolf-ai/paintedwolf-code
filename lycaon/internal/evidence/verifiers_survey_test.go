package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestFileRegionVerifier_surveySnapshotExcerpt(t *testing.T) {
	rec := evidence.Record{
		Kind:       "list",
		Shape:      evidence.ShapeFileRegion,
		SourceTool: "list_dir",
		Survey:     true,
		Path:       "lycaon/internal",
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		Body:       []string{"1\tdir files=12 subdirs=tools entries=foo.go"},
	}
	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{
		Path:    "lycaon/internal",
		Line:    1,
		Excerpt: "subdirs=tools",
	})
	if !ok {
		t.Fatal("survey excerpt must verify against prefixed single-line body")
	}
}

func TestFileRegionVerifier_surveySnapshotWrongLineStillMatchesBody(t *testing.T) {
	rec := evidence.Record{
		Kind:       "find",
		Shape:      evidence.ShapeFileRegion,
		SourceTool: "find",
		Survey:     true,
		Path:       "pkg/a.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		Body:       []string{"1\tfile pkg/a.go"},
	}
	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{
		Path:    "pkg/a.go",
		Line:    99,
		Excerpt: "file pkg/a.go",
	})
	if !ok {
		t.Fatal("survey fallback should match excerpt in body when line is wrong")
	}
}
