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
