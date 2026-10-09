package contract

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// commandPlanCase is one command-surface call. Output names the file the
// reporting stages write, or "" for the result tail; Tail overrides the
// output derived from the approved printf stages.
type commandPlanCase struct {
	name   string
	args   map[string]any
	output string
	tail   string
	reads  []string
}

// commandPlanCorpus exercises every command-plan feature that rewrites or
// binds arguments: globs, scratch addresses, stream redirections, pipes,
// sequences, and cwd.
// Reporting stages print one <arg> line per argv entry they receive.
func commandPlanCorpus() []commandPlanCase {
	return []commandPlanCase{
		{name: "glob", args: map[string]any{"command": `printf '<%s>\n' *.log`}},
		{name: "quoted glob", args: map[string]any{"command": `printf '<%s>\n' '*.log'`}},
		{name: "stdout redirect", args: map[string]any{"command": `printf '<%s>\n' *.log > out.txt`}, output: "out.txt"},
		{name: "stdout append", args: map[string]any{"command": `printf '<%s>\n' a.log >> out.txt`}, output: "out.txt"},
		{name: "stderr redirect", args: map[string]any{"command": `printf '<%s>\n' b.log 2> err.txt`}},
		{name: "stdin redirect", args: map[string]any{"command": `cat < in.txt`}, tail: "in\n", reads: []string{"in.txt"}},
		{name: "pipe", args: map[string]any{"command": `printf '<%s>\n' *.log | cat`}},
		{name: "sequence", args: map[string]any{"command": `printf '<%s>\n' a.log && printf '<%s>\n' sub/*.txt`}},
		{name: "pipeline arg", args: map[string]any{"pipeline": []any{`printf '<%s>\n' *.log`, "cat"}}},
		{name: "cwd glob redirect", args: map[string]any{"command": `printf '<%s>\n' *.txt > out.txt`, "cwd": "sub"}, output: "sub/out.txt"},
		{name: "scratch operand", args: map[string]any{"command": `printf '<%s>\n' @scratch/notes.md '@scratch/literal'`}},
	}
}

// seedCommandTree writes the files the corpus names.
func seedCommandTree(t *testing.T, root string) {
	t.Helper()
	for rel, body := range map[string]string{
		"a.log": "a", "b.log": "b", ".hidden.log": "h", "in.txt": "in\n",
		"sub/c.txt": "c", "sub/d.txt": "d",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "seed dir for "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "seed "+rel, os.WriteFile(path, []byte(body), 0o644))
	}
}

// cloneArgs deep-copies JSON-shaped arguments.
func cloneArgs(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = cloneArgs(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneArgs(item)
		}
		return out
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}

func argsUnchanged(before, after map[string]any) bool {
	return reflect.DeepEqual(before, after)
}

// recordingGate reviews silently and keeps every action it was shown.
type recordingGate struct {
	mu      sync.Mutex
	actions []hitl.ProposedAction
}

func (g *recordingGate) Evaluate(_ context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actions = append(g.actions, action)
	return &hitl.ApprovalResult{}, nil
}

// reviewed returns the last action the gate saw for tool.
func (g *recordingGate) reviewed(tool string) (hitl.ProposedAction, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.actions) - 1; i >= 0; i-- {
		if g.actions[i].Tool == tool {
			return g.actions[i], true
		}
	}
	return hitl.ProposedAction{
}, false
}

func (g *recordingGate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actions = nil
}

func (*recordingGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

func (*recordingGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (*recordingGate) ApplyGrant(hitl.ApprovalGrant) (bool, error)         { return false, nil }
func (*recordingGate) GrantCovers(hitl.ProposedAction) bool                { return false }
func (*recordingGate) HostResourceLeaseCovers(hitl.ProposedAction) bool    { return false }
func (*recordingGate) RevokeGrant(string) (bool, error)                    { return false, nil }
func (*recordingGate) RevokeGrantInstalledBy(string, string) (bool, error) { return false, nil }
func (*recordingGate) ListGrants(string) []hitl.ApprovalGrant              { return nil }
func (*recordingGate) SecretFingerprintsCovered(string, string, string, string, []string) bool {
	return false
}
func (*recordingGate) SecretRedactionStanding(string, []string) bool { return false }
func (*recordingGate) PutAskQuiet(hitl.AskQuiet, int) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (*recordingGate) AskQuietLive(string, string) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (*recordingGate) NoteAskQuietSuppressed(string, string)         {}
func (*recordingGate) ListAskQuiets(string) []hitl.AskQuiet          { return nil }
func (*recordingGate) RevokeAskQuiet(string) bool                    { return false }
func (*recordingGate) RevokeAskQuietInstalledBy(string, string) bool { return false }
func (*recordingGate) ForgetSession(string)                          {}
