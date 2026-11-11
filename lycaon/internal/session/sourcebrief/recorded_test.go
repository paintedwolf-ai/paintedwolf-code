package sourcebrief

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type briefRecords struct {
	Store
	records map[string]string
	err     error
}

func (s briefRecords) TurnSourceBriefs(context.Context, string) (map[string]string, error) {
	return s.records, s.err
}

func TestRecordedBriefPreservesOpeningAndRejectsCorruptRows(t *testing.T) {
	brief := inject.SourceChangeBrief{OtherFiles: 2, OtherEffects: 3}
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	s := New(briefRecords{records: map[string]string{"opening": string(raw), "corrupt": "{", "empty": "{}"}}, nil)
	got := s.Recorded(t.Context(), &api.Session{ID: "session"})
	if len(got) != 1 || got["opening"].OtherFiles != 2 || got["opening"].OtherEffects != 3 {
		t.Fatalf("recorded briefs = %+v", got)
	}
	if s.Recorded(t.Context(), nil) != nil {
		t.Fatal("brief returned without session")
	}
}

func TestACBRF06BriefStoredWithoutWorkingSetYieldsTheSameRows(t *testing.T) {
	const stored = `{"files":[{"path":"a.go","actor":"user","op":"write","at":"14:02 UTC","effects":2}],"other_files":3,"other_effects":4}`
	s := New(briefRecords{records: map[string]string{"opening": stored}}, nil)
	got := s.Recorded(t.Context(), &api.Session{ID: "session"})["opening"]
	want := inject.SourceChangeFile{Path: "a.go", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 2}
	if len(got.Files) != 1 || got.Files[0] != want || got.OtherFiles != 3 || got.OtherEffects != 4 {
		t.Fatalf("recorded brief = %+v", got)
	}
	if got.OtherMayIncludeWorkingSet() {
		t.Fatal("a stored brief's counted files were outside the working set")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != stored {
		t.Fatalf("round trip = %s, want the stored bytes", raw)
	}
}
