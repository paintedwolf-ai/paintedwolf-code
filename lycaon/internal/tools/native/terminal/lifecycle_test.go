package terminal

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestLifecycleRejectionRetainsProcessKindAndCapacity(t *testing.T) {
	capacity := &bgprocess.BackgroundCapacityError{Limit: 9, TerminalIDs: []string{"pty-1"}, CommandHandles: []string{"command-1"}}
	for _, tc := range []struct {
		err    error
		code   string
		reason string
	}{
		{bgprocess.ErrNotPTY, "TERMINAL_NOT_FOUND", "not_pty"},
		{bgprocess.ErrProcessNotFound, "TERMINAL_NOT_FOUND", "not_found"},
		{bgprocess.ErrProcessNotRunning, "TERMINAL_NOT_RUNNING", "not_running"},
		{capacity, "TERMINAL_CAP_REACHED", "cap_reached"},
	} {
		reject := tools.AsToolReject(mapTerminalLifecycleReject(tc.err, "fixture-id"))
		if reject == nil || reject.Code != tc.code || reject.Data["terminal_failure"] != tc.reason || reject.Data["terminal_not_pty"] != errors.Is(tc.err, bgprocess.ErrNotPTY) || reject.Data["id"] != "fixture-id" {
			t.Fatalf("lifecycle observation for %v: %+v", tc.err, reject)
		}
		if errors.Is(tc.err, capacity) && (reject.Data["background_limit"] != capacity.Limit || !reflect.DeepEqual(reject.Data["live_terminal_ids"], capacity.TerminalIDs) || !reflect.DeepEqual(reject.Data["live_command_handles"], capacity.CommandHandles)) {
			t.Fatalf("capacity observation lost admission state: %+v", reject.Data)
		}
	}
}
