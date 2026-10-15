package command

import (
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"strings"
	"sync"
)

// CommandFailureTracker records repeated failing confined invocations per session.
type CommandFailureTracker struct {
	mu       sync.Mutex
	failures map[string]commandFailureState
}

type commandFailureState struct {
	LastApproach string
	Count        int
}

// NewCommandFailureTracker constructs a shared failure tracker for command/verify tools.
func NewCommandFailureTracker() *CommandFailureTracker {
	return &CommandFailureTracker{failures: make(map[string]commandFailureState)}
}

// commandFailureApproachKey includes capability requests and proxy settings
// so a changed boundary counts as a new attempt.
func commandFailureApproachKey(args map[string]any) string {
	cwd, _ := args["cwd"].(string)
	socks, _ := args["socks_proxy"].(bool)
	payload, err := surveyjson.Marshal(struct {
		Command    string `json:"command"`
		Cwd        string `json:"cwd,omitempty"`
		SocksProxy bool   `json:"socks_proxy,omitempty"`
		Capability any    `json:"capability_request,omitempty"`
	}{
		Command:    commandsurface.PrimaryCommandLine(args, nil),
		Cwd:        strings.TrimSpace(cwd),
		SocksProxy: socks,
		Capability: args["capability_request"],
	})
	if err != nil {
		return commandsurface.PrimaryCommandLine(args, nil)
	}
	return string(payload)
}

// rejectLoop blocks a command/verify invocation that has already failed with
// the same approach 3 or more times in a row.
func (ft *CommandFailureTracker) rejectLoop(sessionID, approach, command string) *toolrejection.ToolReject {
	if ft == nil || approach == "" {
		return nil
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	st := ft.failures[sessionID]
	if approach != st.LastApproach {
		return nil
	}
	if st.Count < 3 {
		return nil
	}
	return &toolrejection.ToolReject{
		Code: "COMMAND_FAILURE_LOOP",
		Data: map[string]any{
			"command": command,
			"count":   st.Count,
		},
	}
}

func (ft *CommandFailureTracker) record(sessionID, approach string, ok bool) {
	if ft == nil {
		return
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	st := ft.failures[sessionID]
	if ok {
		st.LastApproach = ""
		st.Count = 0
	} else if approach == st.LastApproach {
		st.Count++
	} else {
		st.LastApproach = approach
		st.Count = 1
	}
	ft.failures[sessionID] = st
}
