package wiring

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// refusalWitness is one call that changes the world when it is allowed.
type refusalWitness struct {
	// covers names the catalog tool or operation, or the command form, it stands for.
	covers string
	tool   string
	args   map[string]any
	// setup runs first, allowed, so the witness has something to act on.
	setup []llm.MockToolCall
}

func (w refusalWitness) rule(effect string) string {
	if w.tool == "command" {
		return "  - category: command\n    pattern: \"*\"\n    effect: " + effect + "\n"
	}
	return "  - category: tool\n    pattern: " + w.tool + "\n    effect: " + effect + "\n"
}

// refusalMode is one way the host refuses a call.
type refusalMode struct {
	name string
	// rules are appended to the project's approval rules.
	rules func(refusalWitness) string
	// readOnly binds the session to a read-only tool profile before the witness.
	readOnly bool
	// decide answers each approval card the witness raises.
	decide func(t *testing.T, s *commandProgramSession, card wire.CheckpointEvent)
	// card is the checkpoint status the refusal leaves; empty when no card is raised.
	card string
}

func declineCard(t *testing.T, s *commandProgramSession, card wire.CheckpointEvent) {
	t.Helper()
	_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(s.h.OwnerCtx(t, t.Context()), s.session, card.ID,
		wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "decline approval", err)
}

func approveCard(t *testing.T, s *commandProgramSession, card wire.CheckpointEvent) {
	t.Helper()
	_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(s.h.OwnerCtx(t, t.Context()),
		s.session, card.ID, card.ToolApproval.Plan.RecommendedOptionID)
	testutil.FailErr(t, "approve card", err)
}

var refusalModes = []refusalMode{
	{name: "read-only profile", rules: func(refusalWitness) string { return "" }, readOnly: true, decide: declineCard},
	{name: "policy deny", rules: func(w refusalWitness) string { return w.rule("deny") }, decide: declineCard},
	{name: "human reject", rules: func(w refusalWitness) string { return w.rule("ask") }, decide: declineCard, card: "rejected"},
	{name: "cancelled approval", rules: func(w refusalWitness) string { return w.rule("ask") }, card: "canceled",
		decide: func(t *testing.T, s *commandProgramSession, _ wire.CheckpointEvent) {
			t.Helper()
			testutil.FailErr(t, "cancel approval", s.h.Sessions.Checkpoints.(*hitl.Checkpoints).CancelPendingForSession(
				s.h.OwnerCtx(t, t.Context()), s.session, "stopped by the test"))
		}},
	// The expiry window is injected per host; the card settles on its own.
	{name: "expired approval", rules: func(w refusalWitness) string { return w.rule("ask") }, card: "expired",
		decide: func(*testing.T, *commandProgramSession, wire.CheckpointEvent) {}},
}

// refusalProject is a project directory with a sibling directory outside the roots.
type refusalProject struct {
	project, outside string
	fetchURL         string
}

func newRefusalProject(t *testing.T, fetchURL string) refusalProject {
	t.Helper()
	base := t.TempDir()
	p := refusalProject{project: filepath.Join(base, "proj"), outside: filepath.Join(base, "outsidé"), fetchURL: fetchURL}
	files := map[string]string{
		"notes.txt":             "first note\nsecond note\n",
		"tracked.txt":           "committed\n",
		"a.go":                  "package a\n\nfunc Old() {}\n",
		"a.json":                "{\"a\":1}\n",
		"sub/keep.txt":          "kept\n",
		"../outsidé/target.txt": "outside canary 5c1d7a\n",
	}
	for rel, body := range files {
		path := filepath.Join(p.project, rel)
		testutil.FailErr(t, "create fixture dir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(body), 0o644))
	}
	writeFixtureTar(t, filepath.Join(p.project, "archive.tar"), "x.txt", "extracted\n")
	gittest.InitCommit(t, p.project, "base")
	gittest.Run(t, p.project, "checkout", "-q", "-b", "side")
	testutil.FailErr(t, "write side file", os.WriteFile(filepath.Join(p.project, "side.txt"), []byte("side\n"), 0o644))
	gittest.CommitAll(t, p.project, "side")
	gittest.Run(t, p.project, "checkout", "-q", "-")
	// A dirty tracked file gives commit, restore and stash something to act on.
	testutil.FailErr(t, "dirty tracked file", os.WriteFile(filepath.Join(p.project, "tracked.txt"), []byte("edited\n"), 0o644))
	return p
}

func writeFixtureTar(t *testing.T, path, name, body string) {
	t.Helper()
	f, err := os.Create(path)
	testutil.FailErr(t, "create archive", err)
	w := tar.NewWriter(f)
	testutil.FailErr(t, "archive header", w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err = w.Write([]byte(body))
	testutil.FailErr(t, "archive body", err)
	testutil.FailErr(t, "close archive", w.Close())
	testutil.FailErr(t, "close archive file", f.Close())
}

// mutatingWitnesses holds one witness per mutating name the catalog declares,
// plus the command forms that reach the tree without a named operation.
func mutatingWitnesses(t *testing.T, fetchURL string) []refusalWitness {
	t.Helper()
	command := func(covers, program string) refusalWitness {
		return refusalWitness{covers: covers, tool: "command", args: map[string]any{"command": program}}
	}
	return []refusalWitness{
		command("write", "printf changed > notes.txt"),
		command("tee", "printf changed | tee notes.txt"),
		command("edit", "edit --path notes.txt --old-string first --new-string FIRST"),
		command("replace_lines", "replace-lines --path notes.txt --start 1 --end 1 --content replaced"),
		command("jq_edit", "jq-edit --path a.json --query '.a = 2'"),
		command("code-rewrite", "code-rewrite --pattern Old --rewrite New --path a.go"),
		command("delete", "rm notes.txt"),
		command("copy", "cp notes.txt copy.txt"),
		command("move", "mv notes.txt moved.txt"),
		command("mkdir", "mkdir made"),
		command("chmod", "chmod 600 notes.txt"),
		command("extract_archive", "tar -xf archive.tar"),
		command("git_commit", "git commit -am change"),
		command("git_restore", "git restore tracked.txt"),
		command("git_checkout", "git checkout side"),
		command("git_merge", "git merge side"),
		command("git_stash", "git stash"),
		command("http_request", "curl -s -o fetched.txt "+fetchURL),
		command("sed -i", "sed -i s/first/FIRST/ notes.txt"),
		command("find -delete", "find . -name notes.txt -delete"),
		command("external exec", "go mod init example.com/witness"),
		{covers: "restore_version", tool: "restore_version", args: map[string]any{"path": "notes.txt"},
			setup: []llm.MockToolCall{
				{ID: "setup-1", Name: "command", Args: map[string]any{"command": "edit --path notes.txt --old-string second --new-string SECOND"}},
				{ID: "setup-2", Name: "command", Args: map[string]any{"command": "cat notes.txt"}},
			}},
		{covers: "code_rewrite", tool: "code_rewrite", args: map[string]any{"pattern": "Old", "rewrite": "New", "path": "a.go"}},
		{covers: "verify", tool: "verify", args: map[string]any{"command": "go mod init example.com/verify"}},
		{covers: "terminal_open", tool: "terminal_open", args: map[string]any{"command": "go mod init example.com/terminal"}},
	}
}

// unwitnessedMutators are catalog mutators no wiring host can run, each with why.
var unwitnessedMutators = map[string]string{
	"render_view": "its dest write needs the bundled browser, which wiring hosts do not provision",
	"chown":       "the host refuses every ownership target an unprivileged test user could choose (CHOWN_TARGET_DENIED)",
}

// catalogMutatingNames are the tools and operations the native catalog says
// change the tree, less the host-produced ones no caller can name.
func catalogMutatingNames(t *testing.T) []string {
	t.Helper()
	catalog, err := nativemanifest.Load()
	testutil.FailErr(t, "load native catalog", err)
	set := map[string]bool{}
	for _, name := range append(catalog.PathMutatingTools(), catalog.WorkerBranchTools()...) {
		if !catalog.HasHostProducedTool(name) {
			set[name] = true
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestRefusedOperationsLeaveTheWorldUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fetched\n")) }))
	t.Cleanup(server.Close)
	witnesses := mutatingWitnesses(t, server.URL)
	gitengine.TestingEnableBundledBinary()
	covered := map[string]bool{"command": true}
	for _, w := range witnesses {
		covered[w.covers] = true
	}
	catalog := catalogMutatingNames(t)
	for _, name := range catalog {
		if !covered[name] && unwitnessedMutators[name] == "" {
			t.Errorf("catalog names mutating %q and no witness exercises it", name)
		}
	}
	for name := range unwitnessedMutators {
		if !slices.Contains(catalog, name) || covered[name] {
			t.Errorf("unwitnessed mutator %q is not a catalog mutator without a witness", name)
		}
	}
	t.Logf("generator A: %d witnesses x %d refusal modes, each with an allowed control", len(witnesses), len(refusalModes))

	controlHost := newProgramHost(t)
	t.Run("allowed", func(t *testing.T) {
		for _, w := range witnesses {
			t.Run(w.covers, func(t *testing.T) {
				t.Parallel()
				before, after, s := runWitness(t, controlHost, w, refusalMode{rules: func(refusalWitness) string { return "" }, decide: approveCard}, server.URL)
				if len(before.diff(after)) == 0 {
					t.Fatalf("witness %s changed nothing when allowed, so its refusal cells prove nothing: %s", w.covers, describeWitness(t, s, w))
				}
			})
		}
	})
	for _, mode := range refusalModes {
		host := newProgramHost(t)
		if mode.card == "expired" {
			host.h.Sessions.Checkpoints.(*hitl.Checkpoints).SetCheckpointExpiry(func() time.Duration { return time.Nanosecond })
		}
		t.Run(mode.name, func(t *testing.T) {
			for _, w := range witnesses {
				t.Run(w.covers, func(t *testing.T) {
					t.Parallel()
					before, after, s := runWitness(t, host, w, mode, server.URL)
					if changed := before.diff(after); len(changed) > 0 {
						t.Errorf("refused %s changed %v: %s", w.covers, changed, describeWitness(t, s, w))
					}
					requireRefusalEvidence(t, s, mode, w)
				})
			}
		})
	}
}

// runWitness runs the setup allowed, then the witness under mode, and reads the
// world on each side of the witness.
func runWitness(t *testing.T, host *programHost, w refusalWitness, mode refusalMode, fetchURL string) (worldState, worldState, *commandProgramSession) {
	t.Helper()
	project := newRefusalProject(t, fetchURL)
	config := &llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: "run the program", FollowUpText: "Done."}}}
	s := host.withClientAt(t, project.project, llm.NewMockProvider(config), wire.CreateSessionRequest{}, mode.rules(w))
	s.h.SeedProgress(t, t.Context(), s.session)
	if len(w.setup) > 0 {
		config.Responses[0].ToolCalls = w.setup
		promptDeciding(t, s, approveCard)
	}
	if mode.readOnly {
		testutil.FailErr(t, "bind read-only profile", s.h.Sessions.Manager.Chats.SetAgentType(t.Context(), s.session, "path-explorer"))
	}
	configDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "config dir", err)
	probe := worldProbe{db: s.h.DB, session: s.session, project: project.project, outside: project.outside,
		grantFile: filepath.Join(configDir, settingsoverlay.BasenameApprovals)}
	before := probe.read(t)
	config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: "witness-1", Name: w.tool, Args: w.args}}
	promptDeciding(t, s, mode.decide)
	return before, probe.read(t), s
}

// promptDeciding runs one turn and answers every approval card it raises.
func promptDeciding(t *testing.T, s *commandProgramSession, decide func(*testing.T, *commandProgramSession, wire.CheckpointEvent)) {
	t.Helper()
	stream, unsubscribe, err := s.h.Events.Subscribe(t.Context(), events.Subscription{Project: s.project, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to checkpoints", err)
	defer unsubscribe()
	done := make(chan error, 1)
	go func() { done <- s.prompt(t.Context()) }()
	answered := map[string]bool{}
	for {
		select {
		case err := <-done:
			testutil.FailErr(t, "prompt", err)
			return
		case env := <-stream:
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			raw, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode checkpoint event", err)
			var card wire.CheckpointEvent
			testutil.FailErr(t, "decode checkpoint event", json.Unmarshal(raw, &card))
			if card.SessionID != s.session || card.Status != wire.CheckpointStatusPending ||
				card.Kind != wire.CheckpointKindToolApproval || answered[card.ID] {
				continue
			}
			answered[card.ID] = true
			decide(t, s, card)
		}
	}
}

// requireRefusalEvidence shows the cell reached the refusal it names, so an
// unchanged world is not explained by a witness that never ran.
func requireRefusalEvidence(t *testing.T, s *commandProgramSession, mode refusalMode, w refusalWitness) {
	t.Helper()
	if mode.card != "" {
		rows, err := s.h.DB.QueryContext(t.Context(), `SELECT status FROM checkpoints WHERE session_id = ? AND kind = 'tool_approval'`, s.session)
		testutil.FailErr(t, "read checkpoints", err)
		defer rows.Close()
		var statuses []string
		for rows.Next() {
			var status string
			testutil.FailErr(t, "scan checkpoint", rows.Scan(&status))
			statuses = append(statuses, status)
		}
		testutil.FailErr(t, "iterate checkpoints", rows.Err())
		if !slices.Contains(statuses, mode.card) {
			t.Fatalf("%s under %s left checkpoints %v, want one %s: %s", w.covers, mode.name, statuses, mode.card, describeWitness(t, s, w))
		}
		return
	}
	if w.tool == "command" {
		if outcome := s.outcome(t, "witness-1"); outcome.Refusal == nil {
			t.Fatalf("%s under %s was not refused: %s", w.covers, mode.name, describeWitness(t, s, w))
		}
		return
	}
	result, ok := findWitnessResult(t, s)
	if !ok {
		// A tool the profile does not offer never runs: no result and no receipt.
		var receipts int
		testutil.FailErr(t, "count witness receipts", s.h.DB.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM invocation_receipts WHERE session_id = ? AND tool_call_id = 'witness-1'`, s.session).Scan(&receipts))
		if receipts != 0 {
			t.Fatalf("%s under %s left %d receipts and no result", w.covers, mode.name, receipts)
		}
		return
	}
	if result.Outcome != wire.ToolResultOutcomeRejected {
		t.Fatalf("%s under %s was not refused: %s", w.covers, mode.name, describeWitness(t, s, w))
	}
}

// describeWitness reports how the witness ended, for a failing cell.
func describeWitness(t *testing.T, s *commandProgramSession, w refusalWitness) string {
	t.Helper()
	result, ok := findWitnessResult(t, s)
	if !ok {
		return "no result"
	}
	if w.tool == "command" {
		outcome := s.outcome(t, "witness-1")
		return fmt.Sprintf("program status %d stop %q refusal %+v nonzero %+v stderr %.300q", outcome.Status, outcome.StopCode,
			outcome.Refusal, outcome.NonzeroExecutions, outcome.Stderr)
	}
	return fmt.Sprintf("outcome %s feedback %+v content %.300q", result.Outcome, result.Feedback, result.Content)
}

func findWitnessResult(t *testing.T, s *commandProgramSession) (*wire.ToolResult, bool) {
	t.Helper()
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleTool && msgs[i].ToolResult != nil && msgs[i].ToolResult.ToolCallID == "witness-1" {
			return msgs[i].ToolResult, true
		}
	}
	return nil, false
}
