package gitadmin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnboundWorktreeViewOmitsState(t *testing.T) {
	view := unboundWorktreeView("11111111-1111-4111-8111-111111111111")
	if view.State != "" {
		t.Fatalf("state = %q, want empty", view.State)
	}
	raw, err := json.Marshal(view)
	testutil.FailErr(t, "marshal view", err)
	if strings.Contains(string(raw), `"state"`) {
		t.Fatalf("unbound view includes state: %s", raw)
	}
}
