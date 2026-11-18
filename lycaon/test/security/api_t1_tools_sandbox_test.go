package security

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestStateToolsRespectProjectDir(t *testing.T) {
	h := wiring.BuildForTest(t)
	sess := createSessionHTTP(t, h.Server, t.TempDir())
	otherDir := t.TempDir()

	out, err := h.ToolRegistry.Run(t.Context(), "state_query", nil, securityToolContext(sess.ID, sess.WorkspacePath, ""))
	testutil.FailErr(t, "state_query from the session's own root", err)
	var state map[string]any
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatalf("state_query output is not a JSON object: %v (%s)", err, out)
	}

	_, err = h.ToolRegistry.Run(t.Context(), "state_query", nil, securityToolContext(sess.ID, otherDir, ""))
	if !errors.Is(err, toolguard.ErrProjectMismatch) {
		t.Fatalf("state_query from another root: err = %v, want ErrProjectMismatch", err)
	}
}
