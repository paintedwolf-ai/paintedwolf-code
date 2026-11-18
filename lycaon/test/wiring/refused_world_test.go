package wiring

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
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

// worldState is what a refused operation must leave as it found. It is read
// with the standard library, git and SQL only, and shares no code with the gate.
type worldState struct {
	project, outside map[string]string
	git              string
	rows             string
	grants           string
}

// worldProbe names what one session can change.
type worldProbe struct {
	db               db.ReadHandle
	session          string
	project, outside string
	grantFile        string
}

func (p worldProbe) read(t *testing.T) worldState {
	t.Helper()
	return worldState{
		project: treeEntries(t, p.project),
		outside: treeEntries(t, p.outside),
		git:     gitDigest(t, p.project) + gitDigest(t, p.outside),
		rows:    p.rowDigest(t),
		grants:  p.grantDigest(t),
	}
}

// diff names each changed path and each other part of the world that changed.
func (w worldState) diff(after worldState) []string {
	changed := append(changedEntries("project", w.project, after.project), changedEntries("outside", w.outside, after.outside)...)
	for _, part := range []struct{ name, before, after string }{
		{"git state", w.git, after.git},
		{"source and effect rows", w.rows, after.rows},
		{"grants", w.grants, after.grants},
	} {
		if part.before != part.after {
			changed = append(changed, part.name)
		}
	}
	return changed
}

func changedEntries(tree string, before, after map[string]string) []string {
	var changed []string
	for rel, digest := range before {
		if after[rel] != digest {
			changed = append(changed, tree+":"+rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			changed = append(changed, tree+":"+rel)
		}
	}
	sort.Strings(changed)
	return changed
}

// treeEntries digests every entry's type, mode, owner, link target and bytes,
// by path. Git's object store is covered by gitDigest instead.
func treeEntries(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	if root == "" {
		return entries
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		sum := sha256.New()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintf(sum, "%d:%d\x00", stat.Uid, stat.Gid)
		}
		fmt.Fprintf(sum, "%s\x00", info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			sum.Write([]byte(target))
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(sum, f)
			_ = f.Close()
			if err != nil {
				return err
			}
		}
		entries[rel] = hex.EncodeToString(sum.Sum(nil))
		return nil
	})
	testutil.FailErr(t, "digest tree "+root, err)
	return entries
}

// gitDigest records HEAD, every ref, the index bytes and porcelain status.
func gitDigest(t *testing.T, dir string) string {
	t.Helper()
	if dir == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	var parts []string
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"for-each-ref", "--format=%(refname) %(objectname)"},
		{"stash", "list"},
		{"status", "--porcelain=v2", "--untracked-files=all"},
	} {
		out, err := worldGit(dir, args...)
		testutil.FailErr(t, "git "+strings.Join(args, " "), err)
		parts = append(parts, out)
	}
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	testutil.FailErr(t, "read git index", err)
	parts = append(parts, hex.EncodeToString(index))
	return strings.Join(parts, "\x00")
}

func worldGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return string(out), nil
}

// rowDigest counts the session's source changes and effect records.
func (p worldProbe) rowDigest(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, query := range []string{
		`SELECT COUNT(*) FROM source_operations WHERE session_id = ?`,
		`SELECT COUNT(*) FROM source_effects e JOIN source_operations o ON o.id = e.operation_id WHERE o.session_id = ?`,
		`SELECT COUNT(*) FROM chat_grants WHERE chat_session_id = ?`,
	} {
		var n int
		testutil.FailErr(t, "count rows", p.db.QueryRowContext(t.Context(), query, p.session).Scan(&n))
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, ",")
}

// grantDigest reads the durable grant file and the session grant rows.
func (p worldProbe) grantDigest(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(p.grantFile)
	if err != nil && !os.IsNotExist(err) {
		testutil.FailErr(t, "read grant file", err)
	}
	rows, err := p.db.QueryContext(t.Context(), `SELECT id, delta_json FROM chat_grants WHERE chat_session_id = ? ORDER BY id`, p.session)
	testutil.FailErr(t, "read session grants", err)
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var id, delta string
		testutil.FailErr(t, "scan grant", rows.Scan(&id, &delta))
		grants = append(grants, id+"="+delta)
	}
	testutil.FailErr(t, "iterate grants", rows.Err())
	sort.Strings(grants)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) + "|" + strings.Join(grants, ";")
}

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
}

func refusalProjectAt(base string) refusalProject {
	return refusalProject{project: filepath.Join(base, "proj"), outside: filepath.Join(base, "outsid\u00e9")}
}

// copyRefusalProject gives one cell its own copy of the template.
func copyRefusalProject(t *testing.T, template refusalProject) refusalProject {
	t.Helper()
	base := filepath.Join(t.TempDir(), "w")
	testutil.FailErr(t, "copy fixture", os.CopyFS(base, os.DirFS(filepath.Dir(template.project))))
	return refusalProjectAt(base)
}

// newRefusalTemplate builds the project and the outside repository once per test.
func newRefusalTemplate(t *testing.T) refusalProject {
	t.Helper()
	p := refusalProjectAt(t.TempDir())
	files := map[string]string{
		"notes.txt":              "first note\nsecond note\n",
		"tracked.txt":            "committed\n",
		"a.go":                   "package a\n\nfunc Old() {}\n",
		"a.json":                 "{\"a\":1}\n",
		"sub/keep.txt":           "kept\n",
		"../outsidé/target.json": "{}\n",
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
	// The outside directory is a repository with a dirty file holding the canary.
	gittest.InitCommit(t, p.outside, "outside")
	testutil.FailErr(t, "dirty outside file", os.WriteFile(filepath.Join(p.outside, "target.json"), []byte(outsideCanary+"\n"), 0o644))
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
	template := newRefusalTemplate(t)

	controlHost := newProgramHost(t)
	t.Run("allowed", func(t *testing.T) {
		for _, w := range witnesses {
			t.Run(w.covers, func(t *testing.T) {
				before, after, s := runWitness(t, controlHost, w, refusalMode{rules: func(refusalWitness) string { return "" }, decide: approveCard}, template)
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
					before, after, s := runWitness(t, host, w, mode, template)
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
func runWitness(t *testing.T, host *programHost, w refusalWitness, mode refusalMode, template refusalProject) (worldState, worldState, *commandProgramSession) {
	t.Helper()
	project := copyRefusalProject(t, template)
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

// outsideCanary is the outside file's content; no refused call may deliver it.
const outsideCanary = `{"canary":"outside 5c1d7a"}`

// pathTemplate is one path-taking tool or operation; {f} is replaced by a
// spelling of the outside file and {d} by a spelling of the outside directory.
type pathTemplate struct {
	covers string
	tool   string
	args   map[string]any
}

func (p pathTemplate) usesDir() bool {
	raw, _ := json.Marshal(p.args)
	return strings.Contains(string(raw), "{d}")
}

func (p pathTemplate) bind(file, dir string) map[string]any {
	raw, _ := json.Marshal(p.args)
	text := strings.NewReplacer("{f}", jsonEscape(file), "{d}", jsonEscape(dir)).Replace(string(raw))
	var out map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return out
}

func jsonEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}

func pathTemplates() []pathTemplate {
	command := func(covers, program string) pathTemplate {
		return pathTemplate{covers: covers, tool: "command", args: map[string]any{"command": program}}
	}
	return []pathTemplate{
		command("read", "cat '{f}'"),
		command("write", "printf x > '{f}'"),
		command("edit", "edit --path '{f}' --old-string canary --new-string CANARY"),
		command("replace_lines", "replace-lines --path '{f}' --start 1 --end 1 --content replaced"),
		command("stat", "stat '{f}'"),
		command("wc", "wc '{f}'"),
		command("delete", "rm '{f}'"),
		command("chmod", "chmod 600 '{f}'"),
		command("chown", "chown "+itoa(os.Getuid())+" '{f}'"),
		command("copy", "cp '{f}' copied.txt"),
		command("move", "mv '{f}' moved.txt"),
		command("mkdir", "mkdir '{d}/made'"),
		command("extract_archive", "tar -xf archive.tar -C '{d}'"),
		command("diff", "diff notes.txt '{f}'"),
		command("jq", "jq . '{f}'"),
		command("jq_edit", "jq-edit --path '{f}' --query '.canary = 1'"),
		command("git_status", "git -C '{d}' status"),
		command("git_diff", "git -C '{d}' diff"),
		command("git_log", "git -C '{d}' log -p"),
		command("git_commit", "git -C '{d}' commit -am outside"),
		command("git_show", "git -C '{d}' show HEAD"),
		command("git_blame", "git -C '{d}' blame target.json"),
		command("git_restore", "git -C '{d}' restore target.json"),
		command("git_ref", "git -C '{d}' rev-parse HEAD"),
		command("git_branches", "git -C '{d}' branch made"),
		command("git_compare", "git -C '{d}' rev-list HEAD"),
		command("git_checkout", "git -C '{d}' checkout -b made"),
		command("git_merge", "git -C '{d}' merge --no-edit HEAD"),
		command("git_stash", "git -C '{d}' stash"),
		command("git_stash_list", "git -C '{d}' stash list"),
		command("find", "find '{d}'"),
		command("grep", "grep -r canary '{d}'"),
		command("list_dir", "ls -la '{d}'"),
		{covers: "code_rewrite", tool: "code_rewrite", args: map[string]any{"pattern": "canary", "rewrite": "x", "lang": "json", "path": "{f}"}},
		{covers: "restore_version", tool: "restore_version", args: map[string]any{"path": "{f}"}},
		{covers: "source_history", tool: "source_history", args: map[string]any{"mode": "version", "path": "{f}"}},
		{covers: "scan_pack", tool: "scan_pack", args: map[string]any{"paths": []any{"{d}"}}},
		{covers: "view_image", tool: "view_image", args: map[string]any{"path": "{f}"}},
		{covers: "view_video", tool: "view_video", args: map[string]any{"path": "{f}"}},
		{covers: "summarize", tool: "summarize", args: map[string]any{"path": "{f}"}},
		{covers: "survey_repo", tool: "survey_repo", args: map[string]any{"bundle": "layout_overview", "path": "{d}"}},
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// unwitnessedPathTools are catalog path tools with no path to escape through.
var unwitnessedPathTools = map[string]string{
	"pack_board": "its plan argument filters the worker roster by Blueprint name and opens no file",
}

// catalogPathNames are the tools and operations the catalog says take paths.
func catalogPathNames(t *testing.T) []string {
	t.Helper()
	catalog, err := nativemanifest.Load()
	testutil.FailErr(t, "load native catalog", err)
	var names []string
	for name, capability := range catalog.MultiRootTools() {
		if capability == "path" || capability == "discovery" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// escapeSpelling names the outside file and directory from inside the project.
type escapeSpelling struct {
	name      string
	file, dir string
}

// escapeSpellings builds each spelling over the real tree. A spelling this
// host's filesystem does not alias names a different, absent path; writing
// there is still an outside write.
func escapeSpellings(t *testing.T, p refusalProject) []escapeSpelling {
	t.Helper()
	base, outsideName := filepath.Dir(p.project), filepath.Base(p.outside)
	target := filepath.Join(p.outside, "target.json")
	testutil.FailErr(t, "link chain", os.Symlink("l2", filepath.Join(p.project, "l1")))
	testutil.FailErr(t, "link chain end", os.Symlink(filepath.Join("..", outsideName), filepath.Join(p.project, "l2")))
	testutil.FailErr(t, "hard link", os.Link(target, filepath.Join(p.project, "hl.json")))
	nfd := filepath.Join(base, strings.TrimSuffix(outsideName, "é")+"é")
	upper := filepath.Join(base, strings.ToUpper(outsideName))
	return []escapeSpelling{
		{name: "parent re-entry", file: filepath.Join("sub", "..", "..", outsideName, "target.json"), dir: filepath.Join("sub", "..", "..", outsideName)},
		{name: "symlink chain", file: filepath.Join("l1", "target.json"), dir: "l1"},
		{name: "hardlink", file: "hl.json"},
		{name: "case variant", file: filepath.Join(upper, "target.json"), dir: upper},
		{name: "NFD variant", file: filepath.Join(nfd, "target.json"), dir: nfd},
		{name: "absolute outside", file: target, dir: p.outside},
	}
}

func TestRefusedPathsStayInsideTheRoots(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	templates := pathTemplates()
	covered := map[string]bool{}
	for _, tpl := range templates {
		covered[tpl.covers] = true
	}
	catalog := catalogPathNames(t)
	for _, name := range catalog {
		if !covered[name] && unwitnessedPathTools[name] == "" {
			t.Errorf("catalog path tool %q has no template", name)
		}
	}
	for name := range unwitnessedPathTools {
		if covered[name] {
			t.Errorf("unwitnessed path tool %q has a template", name)
		}
	}
	writers := catalogMutatingNames(t)
	template := newRefusalTemplate(t)
	host := newProgramHost(t)
	aliases := map[string]bool{}
	for _, name := range []string{"case variant", "NFD variant"} {
		aliases[name] = spellingAliases(t, template, name)
	}
	cells, unaliased := 0, 0
	for _, tpl := range templates {
		writes := slices.Contains(writers, tpl.covers)
		t.Run(tpl.covers, func(t *testing.T) {
			t.Run("allowed absolute", func(t *testing.T) {
				s, changed, delivered, cards := runPathCell(t, host, template, tpl, "absolute outside", approveCard)
				if len(changed) == 0 && !delivered && cards == 0 {
					t.Fatalf("%s reached nothing outside when allowed, so its refusal cells prove nothing: %s", tpl.covers, describePath(t, s, tpl))
				}
			})
			for _, name := range []string{"parent re-entry", "symlink chain", "hardlink", "case variant", "NFD variant", "absolute outside"} {
				if name == "hardlink" && tpl.usesDir() {
					continue // A directory has no hard link.
				}
				// A spelling this filesystem does not alias names an absent path,
				// which only a write can reach.
				if alias, known := aliases[name]; !writes && known && !alias {
					unaliased++
					continue
				}
				cells++
				t.Run(name, func(t *testing.T) {
					s, changed, delivered, _ := runPathCell(t, host, template, tpl, name, declineCard)
					// A hard link is inside the roots, so reading it is not an escape.
					if delivered && name != "hardlink" {
						t.Errorf("%s through %s delivered the outside file", tpl.covers, name)
					}
					if len(changed) > 0 {
						t.Errorf("%s through %s changed %v: %s", tpl.covers, name, changed, describePath(t, s, tpl))
					}
				})
			}
		})
	}
	t.Logf("generator B: %d path tools, %d escape cells, %d read cells skipped where this filesystem does not alias the spelling", len(templates), cells, unaliased)
}

// spellingAliases reports whether a spelling names the outside file here.
func spellingAliases(t *testing.T, template refusalProject, name string) bool {
	t.Helper()
	project := copyRefusalProject(t, template)
	for _, spelling := range escapeSpellings(t, project) {
		if spelling.name != name {
			continue
		}
		want, err := os.Stat(filepath.Join(project.outside, "target.json"))
		testutil.FailErr(t, "stat outside file", err)
		got, err := os.Stat(spelling.file)
		return err == nil && os.SameFile(want, got)
	}
	return false
}

// runPathCell runs one templated call through one spelling, answering every
// card with decide, and reports what reached the outside tree.
func runPathCell(t *testing.T, host *programHost, template refusalProject, tpl pathTemplate, spelling string,
	decide func(*testing.T, *commandProgramSession, wire.CheckpointEvent)) (s *commandProgramSession, changed []string, delivered bool, cards int) {
	t.Helper()
	project := copyRefusalProject(t, template)
	var chosen escapeSpelling
	for _, candidate := range escapeSpellings(t, project) {
		if candidate.name == spelling {
			chosen = candidate
		}
	}
	config := &llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: "run the program", FollowUpText: "Done."}}}
	s = host.withClientAt(t, project.project, llm.NewMockProvider(config), wire.CreateSessionRequest{}, "")
	s.h.SeedProgress(t, t.Context(), s.session)
	configDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "config dir", err)
	probe := worldProbe{db: s.h.DB, session: s.session, outside: project.outside,
		grantFile: filepath.Join(configDir, settingsoverlay.BasenameApprovals)}
	before := probe.read(t)
	config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: "witness-1", Name: tpl.tool, Args: tpl.bind(chosen.file, chosen.dir)}}
	promptDeciding(t, s, decide)
	for _, part := range before.diff(probe.read(t)) {
		if strings.HasPrefix(part, "outside:") || part == "git state" {
			changed = append(changed, part)
		}
	}
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	raw, err := json.Marshal(msgs)
	testutil.FailErr(t, "encode transcript", err)
	delivered = strings.Contains(string(raw), "outside 5c1d7a")
	testutil.FailErr(t, "count cards", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM checkpoints WHERE session_id = ? AND kind = 'tool_approval'`, s.session).Scan(&cards))
	return s, changed, delivered, cards
}

func describePath(t *testing.T, s *commandProgramSession, tpl pathTemplate) string {
	t.Helper()
	return describeWitness(t, s, refusalWitness{covers: tpl.covers, tool: tpl.tool})
}
