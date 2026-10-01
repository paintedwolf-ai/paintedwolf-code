package tooloutput_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestInlineOverlayPromoteJSONStripsConflictsKeepsDigest(t *testing.T) {
	raw := `{"job_id":"job-1","conflicts":[{"path":"a.go","hunks":[{"start_line":1,"end_line":2,"primary":"p","branch":"b"}]}]}`
	compact, ok := tooloutput.InlineOverlayPromoteJSON(raw)
	if !ok {
		t.Fatal("expected compact output")
	}
	if strings.Contains(compact, `"conflicts"`) {
		t.Fatalf("compact should drop conflicts array: %q", compact)
	}
	if !strings.Contains(compact, `"conflict_digest"`) {
		t.Fatalf("compact should keep conflict_digest: %q", compact)
	}
	if strings.Contains(compact, `"hunks"`) {
		t.Fatalf("compact must not include hunk bodies: %q", compact)
	}
}

func TestInlineOverlayPromoteJSONKeepsScopedPathHunks(t *testing.T) {
	raw := `{"job_id":"job-1","paths":["a.go"],"conflicts":[{"path":"a.go","hunks":[{"start_line":1,"end_line":2,"primary":"p","branch":"b"}]}],"path_status":[{"path":"a.go","status":"conflict","hunk_count":2}]}`
	compact, ok := tooloutput.InlineOverlayPromoteJSON(raw)
	if ok {
		t.Fatalf("scoped single-path preview should keep inline conflicts, got compact: %q", compact)
	}
}
