package command

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CommandFailureTracker records repeated failing confined invocations per session.
type CommandFailureTracker struct {
	mu       sync.Mutex
	failures map[string]commandFailureState
}

type commandFailureState struct {
	LastApproach string
	Count        int
	Token        uint64
	Source       commandSourceIdentity
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

// commandSourceIdentity compares admitted content, not watcher activity or intent.
type commandSourceIdentity struct {
	Revision string
	Root     string
}

func (s commandSourceIdentity) known() bool { return s.Revision != "" && s.Root != "" }

func commandSourceReader(ctx context.Context, tctx tools.ToolContext, root string) func() commandSourceIdentity {
	return sync.OnceValue(func() commandSourceIdentity {
		revision, root := sourceledger.VerificationState(ctx, tctx.Source.Observations, root)
		return commandSourceIdentity{revision, root}
	})
}

// commandSourceReaders shares one capture when failure and receipt scopes agree.
func commandSourceReaders(ctx context.Context, tctx tools.ToolContext, cwd string) (failure, verification func() commandSourceIdentity) {
	root := commandFailureSourceRoot(tctx, cwd)
	failure = commandSourceReader(ctx, tctx, root)
	verification = failure
	if receiptRoot := tools.HostWriteRoot(tctx); receiptRoot != root {
		verification = commandSourceReader(ctx, tctx, receiptRoot)
	}
	return failure, verification
}

// commandFailureSourceRoot excludes scratch and preserves worker isolation.
func commandFailureSourceRoot(tctx tools.ToolContext, cwd string) string {
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		rel, err := filepath.Rel(branch, cwd)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return branch
		}
		return ""
	}
	_, root, err := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, cwd)
	if err != nil {
		return ""
	}
	return root.Path
}

type commandFailureAttempt struct {
	Session  string
	Approach string
	Token    uint64
	Count    int
	Source   commandSourceIdentity
}

// begin compares source only when the next outcome can saturate the streak.
// Snapshot I/O never holds the tracker lock. A concurrent admission invalidates
// the comparison rather than granting a retry from an obsolete baseline.
func (ft *CommandFailureTracker) begin(sessionID, approach, command string, observe func() commandSourceIdentity) (commandFailureAttempt, *toolrejection.ToolReject) {
	attempt := commandFailureAttempt{Session: sessionID, Approach: approach}
	if ft == nil || approach == "" {
		return attempt, nil
	}
	ft.mu.Lock()
	before := ft.failures[sessionID]
	ft.mu.Unlock()
	var current commandSourceIdentity
	if before.LastApproach == approach && before.Count >= 2 && observe != nil {
		current = observe()
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	state := ft.failures[sessionID]
	if state.Token != before.Token {
		current = commandSourceIdentity{}
	}
	if state.LastApproach == approach {
		attempt.Count = state.Count
		if state.Count >= 3 {
			known := state.Source.known() && current.known()
			if !known || state.Source == current {
				return attempt, &toolrejection.ToolReject{Code: "COMMAND_FAILURE_LOOP", Data: map[string]any{
					"command": command, "count": state.Count, "failure_source_unchanged": known,
				}}
			}
			attempt.Count = 0
		}
	}
	state.Token++
	attempt.Token = state.Token
	attempt.Source = current
	ft.failures[sessionID] = state
	return attempt, nil
}

// record ignores outcomes from an admission superseded by a later invocation.
func (ft *CommandFailureTracker) record(attempt commandFailureAttempt, ok bool) {
	if ft == nil || attempt.Token == 0 {
		return
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	state := ft.failures[attempt.Session]
	if state.Token != attempt.Token {
		return
	}
	state.Token++
	state.LastApproach = attempt.Approach
	state.Count = attempt.Count + 1
	state.Source = attempt.Source
	if ok {
		state.LastApproach = ""
		state.Count = 0
		state.Source = commandSourceIdentity{}
	}
	ft.failures[attempt.Session] = state
}
