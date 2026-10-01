//go:build integration

package projectremoval

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFailedDeletionReportsObservedProjectState(t *testing.T) {
	for _, state := range []string{"retained", "deleted", "unknown"} {
		t.Run(state, func(t *testing.T) {
			o, reg, id := fixture(t)
			install(t, o, id, "acme/one")
			req := review(t, o, id, "acme/one")
			o.Delete = func(context.Context, string, bool) error {
				switch state {
				case "deleted":
					delete(reg.rows, id)
				case "unknown":
					reg.getError = errors.New("registry unavailable")
				}
				return errors.New("deletion failed")
			}
			result, err := o.Remove(t.Context(), id, req)
			testutil.FailErr(t, "remove", err)
			if result.ProjectState != state || result.CleanupState != "retained" {
				t.Fatalf("result = %+v; want project %s and retained extensions", result, state)
			}
			assertInstalled(t, o, "acme/one", true)
			retry, err := o.Remove(t.Context(), id, req)
			testutil.FailErr(t, "replay failure", err)
			if retry.ProjectState != result.ProjectState || retry.Reason != result.Reason {
				t.Fatalf("replay changed the recorded outcome: %+v", retry)
			}
		})
	}
}
