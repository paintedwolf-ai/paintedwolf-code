//go:build darwin || linux

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	confinefixture "github.com/lycaon/lycaon/internal/testsetup/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type hostFixtureFacts struct {
	mode  os.FileMode
	uid   uint32
	gid   uint32
	mtime int64
	sum   string
}

func statHostFixture(t *testing.T, path string) hostFixtureFacts {
	t.Helper()
	info, err := os.Lstat(path)
	testutil.FailErr(t, "stat host fixture", err)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read host fixture", err)
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("missing unix stat for %s", path)
	}
	digest := sha256.Sum256(raw)
	return hostFixtureFacts{
		mode:  info.Mode(),
		uid:   st.Uid,
		gid:   st.Gid,
		mtime: info.ModTime().UnixNano(),
		sum:   hex.EncodeToString(digest[:]),
	}
}

func makeSharedTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", prefix) //nolint:usetesting // Acceptance fixtures target shared machine temp.
	testutil.FailErr(t, "create shared temp dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return fspath.CanonicalPath(dir)
}

func sessionScratchMarker(t *testing.T, sessionID string) string {
	t.Helper()
	root, err := configdir.Root()
	testutil.FailErr(t, "config root", err)
	return filepath.Join(enginepaths.SessionScratchUnder(root, sessionID), "effect.marker")
}

func buildInternalTempProbe(t *testing.T) string {
	t.Helper()
	srcDir := outsideCommandFolder(t)
	shebang := "#!/usr/bin/env python3\n"
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/usr/bin/python3"); err == nil {
			// Platform-signed interpreter reports Sandbox.kext violations on first exec.
			shebang = "#!/usr/bin/python3\n"
		}
	}
	script := shebang + `import os
import sys

if len(sys.argv) > 1 and sys.argv[1] == "warm":
    sys.exit(0)

try:
    with open("probe.cfg", "r", encoding="utf-8") as fh:
        lines = fh.read().strip().splitlines()
except OSError:
    sys.exit(2)

mode = lines[0]
target = lines[1]
scratch = os.environ.get("SCRATCH_DIR", "")
if not scratch:
    sys.stderr.write("missing SCRATCH_DIR\n")
    sys.exit(3)

with open(os.path.join(scratch, "effect.marker"), "ab") as fh:
    fh.write(b"1")

if mode == "read":
    try:
        with open(target, "rb") as fh:
            sys.stdout.buffer.write(fh.read())
    except OSError as exc:
        sys.stderr.write(f"{exc}\n")
        sys.exit(1)
elif mode == "tolerated":
    try:
        with open(target, "rb") as fh:
            fh.read()
    except OSError:
        pass
    sys.stdout.write("tolerated-ok\n")
elif mode == "exchange":
    out_path = lines[2]
    try:
        with open(target, "rb") as fh:
            data = fh.read()
        with open(out_path, "wb") as fh:
            fh.write(data + b"processed\n")
    except OSError as exc:
        sys.stderr.write(f"{exc}\n")
        sys.exit(1)
elif mode == "list":
    try:
        entries = sorted(os.listdir(target))
    except OSError as exc:
        sys.stderr.write(f"{exc}\n")
        sys.exit(1)
    for name in entries:
        sys.stdout.write(name + "\n")
elif mode == "write":
    try:
        with open(target, "wb") as fh:
            fh.write(b"process-written\n")
    except OSError as exc:
        sys.stderr.write(f"{exc}\n")
        sys.exit(1)
`
	binPath := filepath.Join(srcDir, "temp-probe")
	testutil.FailErr(t, "write probe script", os.WriteFile(binPath, []byte(script), 0o755))
	out, err := exec.CommandContext(t.Context(), binPath, "warm").CombinedOutput()
	if err != nil {
		t.Fatalf("warm probe: %v\n%s", err, out)
	}
	return binPath
}

// runTurnDeciding submits one tool call on s and answers each pending approval card via decide.
func runTurnDeciding(
	t *testing.T,
	s *replaySession,
	callID, tool string,
	args map[string]any,
	decide func(*testing.T, *commandProgramSession, wire.CheckpointEvent),
) []wire.CheckpointEvent {
	t.Helper()
	s.config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: callID, Name: tool, Args: args}}
	var seen []wire.CheckpointEvent
	promptDeciding(t, s.commandProgramSession, func(t *testing.T, cs *commandProgramSession, card wire.CheckpointEvent) {
		seen = append(seen, card)
		decide(t, cs, card)
	})
	return seen
}

func selectOptionMatching(substr string) func(*testing.T, *commandProgramSession, wire.CheckpointEvent) {
	return func(t *testing.T, s *commandProgramSession, card wire.CheckpointEvent) {
		t.Helper()
		for _, opt := range card.ToolApproval.Plan.Options {
			if strings.Contains(opt.Title, substr) {
				_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(
					s.h.OwnerCtx(t, t.Context()), s.session, card.ID, opt.ID,
				)
				testutil.FailErr(t, "resolve option "+opt.Title, err)
				return
			}
		}
		t.Fatalf("no option matching %q in %+v", substr, card.ToolApproval.Plan.Options)
	}
}

func TestSharedTempBaselineIsolationPreservesHostFixtures(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	shared := makeSharedTempDir(t, "pw-st-baseline-")
	alphaName, betaName := "alpha-hidden-91c2.txt", "beta-hidden-44e8.txt"
	alphaCanary, betaCanary := "canary-alpha-91c2-payload", "canary-beta-44e8-payload"
	alpha := filepath.Join(shared, alphaName)
	beta := filepath.Join(shared, betaName)
	testutil.FailErr(t, "seed alpha", os.WriteFile(alpha, []byte(alphaCanary+"\n"), 0o644))
	testutil.FailErr(t, "seed beta", os.WriteFile(beta, []byte(betaCanary+"\n"), 0o644))
	beforeAlpha := statHostFixture(t, alpha)
	beforeBeta := statHostFixture(t, beta)

	probeBin := buildInternalTempProbe(t)
	host := newProgramHost(t, gate.PostureBalanced)
	s := host.replay(t)

	testutil.FailErr(t, "write probe cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("read\n"+alpha+"\n"), 0o600))
	attempts := []struct {
		id   string
		tool string
		args map[string]any
	}{
		{"b-cat-a", "command", map[string]any{"command": "cat '" + alpha + "'"}},
		{"b-cat-b", "command", map[string]any{"command": "cat '" + beta + "'"}},
		{"b-ls", "command", map[string]any{"command": "ls -la '" + shared + "'"}},
		{"b-grep", "command", map[string]any{"command": "grep -r canary '" + shared + "'"}},
		{"b-find", "command", map[string]any{"command": "find '" + shared + "'"}},
		{"b-glob", "glob_files", map[string]any{"pattern": shared + "/*"}},
		{"b-cp", "command", map[string]any{"command": "cp '" + alpha + "' stolen.txt"}},
		{"b-tar", "command", map[string]any{"command": "tar -cf stolen.tar '" + alpha + "'"}},
		{"b-write", "command", map[string]any{"command": "printf tampered > '" + beta + "'"}},
		{"b-proc-read", "command", map[string]any{"command": "external -- '" + probeBin + "'"}},
	}
	for _, attempt := range attempts {
		runTurnDeciding(t, s, attempt.id, attempt.tool, attempt.args, declineCard)
	}
	testutil.FailErr(t, "write list cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("list\n"+shared+"\n"), 0o600))
	runTurnDeciding(t, s, "b-proc-list", "command", map[string]any{"command": "external -- '" + probeBin + "'"}, declineCard)
	testutil.FailErr(t, "write mutate cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("write\n"+beta+"\n"), 0o600))
	runTurnDeciding(t, s, "b-proc-write", "command", map[string]any{"command": "external -- '" + probeBin + "'"}, declineCard)

	msgs, err := s.h.Store.Transcript.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	for _, msg := range msgs {
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		raw, err := json.Marshal(msg.ToolResult)
		testutil.FailErr(t, "marshal tool result", err)
		text := string(raw)
		if strings.Contains(text, alphaCanary) || strings.Contains(text, betaCanary) {
			t.Fatalf("unapproved shared-temp canary leaked in %s: %s", msg.ToolResult.ToolCallID, text)
		}
		if msg.ToolResult.ToolCallID == "b-ls" || msg.ToolResult.ToolCallID == "b-find" ||
			msg.ToolResult.ToolCallID == "b-glob" || msg.ToolResult.ToolCallID == "b-proc-list" {
			if strings.Contains(text, alphaName) || strings.Contains(text, betaName) {
				t.Fatalf("unapproved directory listing leaked hidden names in %s: %s", msg.ToolResult.ToolCallID, text)
			}
		}
	}
	var grantCount int
	testutil.FailErr(t, "count session grants", s.h.DB.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM chat_grants WHERE chat_session_id = ?`, s.session,
	).Scan(&grantCount))
	if grantCount != 0 {
		t.Fatalf("declined baseline attempts committed %d grants", grantCount)
	}
	if got := statHostFixture(t, alpha); got != beforeAlpha {
		t.Fatalf("alpha fixture changed: got %+v, want %+v", got, beforeAlpha)
	}
	if got := statHostFixture(t, beta); got != beforeBeta {
		t.Fatalf("beta fixture changed: got %+v, want %+v", got, beforeBeta)
	}
}

func TestSharedTempOneDecisionDeclaredExchangeAndDirectionalReuse(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	shared := makeSharedTempDir(t, "pw-st-exchange-")
	inFile := filepath.Join(shared, "in.txt")
	outDir := filepath.Join(shared, "out")
	testutil.FailErr(t, "mkdir outDir", os.MkdirAll(outDir, 0o755))
	outFile := filepath.Join(outDir, "result.txt")
	siblingIn := filepath.Join(shared, "sibling-in.txt")
	siblingOut := filepath.Join(shared, "sibling-out.txt")
	testutil.FailErr(t, "seed input", os.WriteFile(inFile, []byte("exchange-input\n"), 0o600))
	testutil.FailErr(t, "seed sibling in", os.WriteFile(siblingIn, []byte("sibling-secret\n"), 0o600))
	testutil.FailErr(t, "seed sibling out", os.WriteFile(siblingOut, []byte("untouched\n"), 0o600))
	beforeIn := statHostFixture(t, inFile)
	beforeSiblingIn := statHostFixture(t, siblingIn)
	beforeSiblingOut := statHostFixture(t, siblingOut)

	probeBin := buildInternalTempProbe(t)
	host := newProgramHost(t, gate.PostureBalanced)
	s := host.replay(t)
	testutil.FailErr(t, "write exchange cfg", os.WriteFile(
		filepath.Join(s.dir, "probe.cfg"),
		[]byte("exchange\n"+inFile+"\n"+outFile+"\n"), 0o600,
	))
	markerPath := sessionScratchMarker(t, s.session)
	declArgs := map[string]any{
		"command": "external -- '" + probeBin + "'",
		"capability_request": map[string]any{
			"read_path":  inFile,
			"write_root": outDir,
		},
	}

	// Verify preflight holds launch before approval: 0 side effects while pending.
	s.config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: "ex-1", Name: "command", Args: declArgs}}
	stream, unsub, err := s.h.Events.Subscribe(t.Context(), events.Subscription{Project: s.project, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to exchange checkpoints", err)
	defer unsub()
	done := make(chan error, 1)
	go func() { done <- s.prompt(t.Context()) }()
	var card wire.CheckpointEvent
	for card.ID == "" {
		select {
		case err := <-done:
			t.Fatalf("prompt ended before an approval was announced: %v", err)
		case env := <-stream:
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			raw, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode checkpoint event", err)
			var candidate wire.CheckpointEvent
			testutil.FailErr(t, "decode checkpoint event", json.Unmarshal(raw, &candidate))
			if candidate.SessionID == s.session && candidate.Status == wire.CheckpointStatusPending && candidate.Kind == wire.CheckpointKindToolApproval {
				card = candidate
			}
		}
	}
	if _, err := os.Stat(outFile); !os.IsNotExist(err) {
		t.Fatalf("declared exchange wrote output before approval: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("declared exchange started producer before approval: %v", err)
	}
	if len(card.ToolApproval.Plan.Subject.Targets) < 2 {
		t.Fatalf("combined preflight card did not name both read and write targets: %+v", card.ToolApproval.Plan.Subject)
	}
	approveCard(t, s.commandProgramSession, card)
	testutil.FailErr(t, "complete declared exchange", <-done)

	var totalCards int
	testutil.FailErr(t, "count exchange cards", s.h.DB.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM checkpoints WHERE session_id = ? AND kind = 'tool_approval'`, s.session,
	).Scan(&totalCards))
	if totalCards != 1 {
		t.Fatalf("declared exchange raised %d cards, want 1", totalCards)
	}
	outBytes, err := os.ReadFile(outFile)
	testutil.FailErr(t, "read host output", err)
	if string(outBytes) != "exchange-input\nprocessed\n" {
		t.Fatalf("host output = %q", outBytes)
	}
	markers, err := os.ReadFile(markerPath)
	testutil.FailErr(t, "read marker count", err)
	if len(markers) != 1 {
		t.Fatalf("producer invocation count = %d, want 1", len(markers))
	}

	// Mixed native reuse within granted directions requires 0 extra cards, including explicit re-declaration.
	extra := runTurnDeciding(t, s, "ex-native-reuse", "command", map[string]any{
		"command": "cat '" + inFile + "' > '" + filepath.Join(outDir, "native.txt") + "'",
		"capability_request": map[string]any{
			"read_path":  inFile,
			"write_root": outDir,
		},
	}, approveCard)
	if len(extra) != 0 {
		t.Fatalf("reuse within granted exchange directions raised %d extra cards", len(extra))
	}
	nativeBytes, err := os.ReadFile(filepath.Join(outDir, "native.txt"))
	testutil.FailErr(t, "read native output", err)
	if string(nativeBytes) != "exchange-input\n" {
		t.Fatalf("native reuse output = %q", nativeBytes)
	}

	// Write-only outDir cannot be read without a read grant, and siblings stay protected.
	readOutCards := runTurnDeciding(t, s, "ex-read-wo", "command", map[string]any{
		"command": "cat '" + outFile + "'",
	}, declineCard)
	if len(readOutCards) == 0 || s.outcome(t, "ex-read-wo").Status == 0 {
		t.Fatalf("write-only output was readable without read approval: cards=%d outcome=%+v", len(readOutCards), s.outcome(t, "ex-read-wo"))
	}
	runTurnDeciding(t, s, "ex-sib-in", "command", map[string]any{"command": "cat '" + siblingIn + "'"}, declineCard)
	runTurnDeciding(t, s, "ex-sib-out", "command", map[string]any{"command": "printf x > '" + siblingOut + "'"}, declineCard)
	if got := statHostFixture(t, inFile); got != beforeIn {
		t.Fatalf("input modified: %+v != %+v", got, beforeIn)
	}
	if got := statHostFixture(t, siblingIn); got != beforeSiblingIn {
		t.Fatalf("sibling input modified: %+v != %+v", got, beforeSiblingIn)
	}
	if got := statHostFixture(t, siblingOut); got != beforeSiblingOut {
		t.Fatalf("sibling output modified: %+v != %+v", got, beforeSiblingOut)
	}
}

func TestSharedTempExchangeSubdirectoryUnpredictableFilenamesAndWriteOnlyIsolation(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	exchangeDir := makeSharedTempDir(t, "pw-st-subdir-")
	otherDir := makeSharedTempDir(t, "pw-st-other-")
	seed := filepath.Join(exchangeDir, "seed.txt")
	otherSecret := filepath.Join(otherDir, "secret.txt")
	testutil.FailErr(t, "seed exchange file", os.WriteFile(seed, []byte("seed\n"), 0o600))
	testutil.FailErr(t, "seed other secret", os.WriteFile(otherSecret, []byte("other-secret\n"), 0o600))

	probeBin := buildInternalTempProbe(t)
	host := newProgramHost(t, gate.PostureBalanced)
	s := host.replay(t)

	// Card 1: select the exchange subdirectory option ("Read and write <dir> for this chat").
	cards := runTurnDeciding(t, s, "sub-1", "command", map[string]any{
		"command": "cat '" + seed + "'",
	}, selectOptionMatching("Read and write "+exchangeDir+" for this chat"))
	if len(cards) != 1 || s.outcome(t, "sub-1").Stdout != "seed\n" {
		t.Fatalf("initial subdirectory grant failed: cards=%d outcome=%+v", len(cards), s.outcome(t, "sub-1"))
	}

	// Unpredictable new filename created by process, read by native cat, and replaced by native edit with 0 cards.
	unpredictable := filepath.Join(exchangeDir, "artifact-9f31c.txt")
	testutil.FailErr(t, "write create cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("write\n"+unpredictable+"\n"), 0o600))
	if extra := runTurnDeciding(t, s, "sub-create", "command", map[string]any{
		"command": "external -- '" + probeBin + "'",
	}, approveCard); len(extra) != 0 || s.outcome(t, "sub-create").Status != 0 {
		t.Fatalf("process create in granted exchange dir: cards=%d outcome=%+v", len(extra), s.outcome(t, "sub-create"))
	}
	if extra := runTurnDeciding(t, s, "sub-read", "command", map[string]any{
		"command": "cat '" + unpredictable + "'",
	}, approveCard); len(extra) != 0 || s.outcome(t, "sub-read").Stdout != "process-written\n" {
		t.Fatalf("native read in granted exchange dir: cards=%d outcome=%+v", len(extra), s.outcome(t, "sub-read"))
	}
	if extra := runTurnDeciding(t, s, "sub-replace", "command", map[string]any{
		"command": "edit --path '" + unpredictable + "' --old-string process-written --new-string replaced-ok",
	}, approveCard); len(extra) != 0 || s.outcome(t, "sub-replace").Status != 0 {
		t.Fatalf("native edit in granted exchange dir: cards=%d outcome=%+v", len(extra), s.outcome(t, "sub-replace"))
	}
	updated, err := os.ReadFile(unpredictable)
	testutil.FailErr(t, "read replaced file", err)
	if string(updated) != "replaced-ok\n" {
		t.Fatalf("updated bytes = %q", updated)
	}

	// Sibling directory outside exchangeDir stays closed, and an unrelated chat gets no access.
	if sibCards := runTurnDeciding(t, s, "sub-other", "command", map[string]any{
		"command": "cat '" + otherSecret + "'",
	}, declineCard); len(sibCards) == 0 || s.outcome(t, "sub-other").Status == 0 {
		t.Fatalf("sibling directory was accessible: cards=%d outcome=%+v", len(sibCards), s.outcome(t, "sub-other"))
	}
	s2 := host.replay(t)
	if otherChatCards := runTurnDeciding(t, s2, "other-chat", "command", map[string]any{
		"command": "cat '" + unpredictable + "'",
	}, declineCard); len(otherChatCards) == 0 || s2.outcome(t, "other-chat").Status == 0 {
		t.Fatalf("second chat inherited first chat's temp grant: cards=%d", len(otherChatCards))
	}

	// Write-only process grant: allows creating new file, denies reading existing file and listing sibling names.
	woDir := makeSharedTempDir(t, "pw-st-wo-")
	woExisting := filepath.Join(woDir, "existing-secret.txt")
	woCreated := filepath.Join(woDir, "created.txt")
	testutil.FailErr(t, "seed write-only existing", os.WriteFile(woExisting, []byte("do-not-read\n"), 0o600))
	sWO := host.replay(t)
	testutil.FailErr(t, "write wo create cfg", os.WriteFile(filepath.Join(sWO.dir, "probe.cfg"), []byte("write\n"+woCreated+"\n"), 0o600))
	woCards := runTurnDeciding(t, sWO, "wo-grant", "command", map[string]any{
		"command":            "external -- '" + probeBin + "'",
		"capability_request": map[string]any{"write_root": woDir},
	}, approveCard)
	if len(woCards) != 1 || sWO.outcome(t, "wo-grant").Status != 0 {
		t.Fatalf("write-only grant failed to create file: cards=%d outcome=%+v", len(woCards), sWO.outcome(t, "wo-grant"))
	}
	testutil.FailErr(t, "write wo read cfg", os.WriteFile(filepath.Join(sWO.dir, "probe.cfg"), []byte("read\n"+woExisting+"\n"), 0o600))
	if c := runTurnDeciding(t, sWO, "wo-proc-read", "command", map[string]any{"command": "external -- '" + probeBin + "'"}, declineCard); sWO.outcome(t, "wo-proc-read").Status == 0 {
		t.Fatalf("write-only grant allowed process read of existing file: cards=%d", len(c))
	}
	testutil.FailErr(t, "write wo list cfg", os.WriteFile(filepath.Join(sWO.dir, "probe.cfg"), []byte("list\n"+woDir+"\n"), 0o600))
	if c := runTurnDeciding(t, sWO, "wo-proc-list", "command", map[string]any{"command": "external -- '" + probeBin + "'"}, declineCard); sWO.outcome(t, "wo-proc-list").Status == 0 {
		t.Fatalf("write-only grant allowed process directory listing: cards=%d", len(c))
	}
}

func TestSharedTempAutomaticTriggerAndBackgroundStarterSettlement(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	confinefixture.RequireRefusalReports(t)
	shared := makeSharedTempDir(t, "pw-st-auto-")
	internalInput := filepath.Join(shared, "internal-input.txt")
	bgInput := filepath.Join(shared, "bg-input.txt")
	toleratedInput := filepath.Join(shared, "optional-probe.txt")
	testutil.FailErr(t, "seed internal input", os.WriteFile(internalInput, []byte("internal-payload\n"), 0o600))
	testutil.FailErr(t, "seed bg input", os.WriteFile(bgInput, []byte("bg-payload\n"), 0o600))
	testutil.FailErr(t, "seed tolerated input", os.WriteFile(toleratedInput, []byte("optional\n"), 0o600))

	probeBin := buildInternalTempProbe(t)
	host := newProgramHost(t, gate.PostureBalanced)
	s := host.replay(t)

	// Negative controls: missing local file, spoofed stderr text, and tolerated exit-0 temp probe produce 0 cards.
	if cards := runTurnDeciding(t, s, "neg-missing", "command", map[string]any{"command": "cat does-not-exist.txt"}, approveCard); len(cards) != 0 {
		t.Fatalf("missing local file raised %d cards", len(cards))
	}
	if cards := runTurnDeciding(t, s, "neg-spoof", "command", map[string]any{
		"command": "external -- /bin/sh -c 'echo \"/tmp/spoof: Permission denied\" >&2; exit 1'",
	}, approveCard); len(cards) != 0 {
		t.Fatalf("spoofed stderr refusal raised %d cards", len(cards))
	}
	testutil.FailErr(t, "write tolerated cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("tolerated\n"+toleratedInput+"\n"), 0o600))
	if cards := runTurnDeciding(t, s, "neg-tolerated", "command", map[string]any{
		"command": "external -- '" + probeBin + "'",
	}, approveCard); len(cards) != 0 || s.outcome(t, "neg-tolerated").Status != 0 {
		t.Fatalf("tolerated exit-0 temp probe raised %d cards: %+v", len(cards), s.outcome(t, "neg-tolerated"))
	}

	// Automatic foreground trigger: no argv path, no capability_request; side effect recorded once, no auto-replay.
	testutil.FailErr(t, "write auto read cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("read\n"+internalInput+"\n"), 0o600))
	markerPath := sessionScratchMarker(t, s.session)
	_ = os.Remove(markerPath)
	fgCards := runTurnDeciding(t, s, "auto-fg-1", "command", map[string]any{
		"command": "external -- '" + probeBin + "'",
	}, approveCard)
	if len(fgCards) != 1 {
		t.Fatalf("automatic foreground trigger cards = %d, want 1", len(fgCards))
	}
	firstOutcome := s.outcome(t, "auto-fg-1")
	if firstOutcome.Status == 0 {
		t.Fatalf("original failed invocation must remain visible after settlement: %+v", firstOutcome)
	}
	fgMarkers, err := os.ReadFile(markerPath)
	testutil.FailErr(t, "read fg marker", err)
	if len(fgMarkers) != 1 {
		t.Fatalf("host replayed failed child automatically: marker bytes = %d, want 1", len(fgMarkers))
	}
	// Follow-up read completes with 0 additional approval cards.
	if retryCards := runTurnDeciding(t, s, "auto-fg-2", "command", map[string]any{
		"command": "external -- '" + probeBin + "'",
	}, approveCard); len(retryCards) != 0 || s.outcome(t, "auto-fg-2").Stdout != "internal-payload\n" {
		t.Fatalf("follow-up read after automatic approval: cards=%d outcome=%+v", len(retryCards), s.outcome(t, "auto-fg-2"))
	}

	// Background starter variant: job handle returned before child fails on internal temp read.
	testutil.FailErr(t, "write bg read cfg", os.WriteFile(filepath.Join(s.dir, "probe.cfg"), []byte("read\n"+bgInput+"\n"), 0o600))
	gateFIFO := filepath.Join(s.dir, "bg-release.fifo")
	testutil.FailErr(t, "mkfifo bg gate", syscall.Mkfifo(gateFIFO, 0o600))
	stream, unsubscribe, err := s.h.Events.Subscribe(t.Context(), events.Subscription{Project: s.project, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe for background card", err)
	defer unsubscribe()

	s.config.Responses[0].ToolCalls = []llm.MockToolCall{{
		ID: "auto-bg-start", Name: "command",
		Args: map[string]any{
			"command": "external -- /bin/sh -c 'IFS= read -r _ < bg-release.fifo; exec \"" + probeBin + "\"' &",
		},
	}}
	testutil.FailErr(t, "start background job", s.prompt(t.Context()))
	go func() {
		f, openErr := os.OpenFile(gateFIFO, os.O_WRONLY, 0)
		if openErr == nil {
			_, _ = f.WriteString("go\n")
			_ = f.Close()
		}
	}()
	var bgCard wire.CheckpointEvent
	for bgCard.ID == "" {
		select {
		case <-t.Context().Done():
			t.Fatalf("timed out waiting for background checkpoint: %v", t.Context().Err())
		case env := <-stream:
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			raw, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode bg checkpoint", err)
			var candidate wire.CheckpointEvent
			testutil.FailErr(t, "decode bg checkpoint", json.Unmarshal(raw, &candidate))
			if candidate.SessionID == s.session && candidate.Status == wire.CheckpointStatusPending && candidate.Kind == wire.CheckpointKindToolApproval {
				bgCard = candidate
			}
		}
	}
	approveCard(t, s.commandProgramSession, bgCard)
	testutil.WaitFor(t, "background approval delivers completion guidance", func() bool {
		_ = s.h.Sessions.Manager.Coordinator.Guidance.DeliverPending(t.Context(), s.session)
		msgs, err := s.h.Store.Transcript.GetMessages(t.Context(), s.session)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if strings.Contains(m.Content, bgInput) && strings.Contains(m.Content, "SANDBOX_SHARED_TEMP_REFUSED") {
				return true
			}
		}
		return false
	})
	if bgFollow := runTurnDeciding(t, s, "auto-bg-follow", "command", map[string]any{
		"command": "cat '" + bgInput + "'",
	}, approveCard); len(bgFollow) != 0 || s.outcome(t, "auto-bg-follow").Stdout != "bg-payload\n" {
		t.Fatalf("follow-up read after background approval: cards=%d outcome=%+v", len(bgFollow), s.outcome(t, "auto-bg-follow"))
	}
}

func TestSharedTempIndependentSocketFloorTwoCardSequence(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	shared := makeSharedTempDir(t, "pw-st-2step-")
	confFile := filepath.Join(shared, "service.conf")
	testutil.FailErr(t, "seed service conf", os.WriteFile(confFile, []byte("/healthz\n"), 0o600))
	sockPath, hits := serveDaemonSocket(t)

	host := newProgramHost(t, gate.PostureBalanced)
	s := host.replay(t)

	// Step 1: Approve reading the shared-temp file (Card 1).
	step1Cards := runTurnDeciding(t, s, "step-1-file", "command", map[string]any{
		"command": "cat '" + confFile + "'",
	}, selectOptionMatching("Read and write "+shared+" for this chat"))
	if len(step1Cards) != 1 || s.outcome(t, "step-1-file").Stdout != "/healthz\n" {
		t.Fatalf("step 1 file card = %d outcome=%+v", len(step1Cards), s.outcome(t, "step-1-file"))
	}
	if hits.Load() != 0 {
		t.Fatalf("socket received %d hits before socket approval", hits.Load())
	}

	// Even if a Unix socket lives in shared temp, a temp grant alone cannot connect without socket approval.
	inSharedSockDir := filepath.Join(shared, "sock")
	testutil.FailErr(t, "mkdir sock dir", os.MkdirAll(inSharedSockDir, 0o755))
	sockLink := filepath.Join(inSharedSockDir, "daemon.sock")
	testutil.FailErr(t, "symlink daemon sock into granted temp dir", os.Symlink(sockPath, sockLink))
	declinedSockCards := runTurnDeciding(t, s, "step-1b-unapproved-sock", "command", map[string]any{
		"command": "curl -s --max-time 5 --unix-socket '" + sockLink + "' http://localhost/healthz",
	}, declineCard)
	if len(declinedSockCards) != 1 || hits.Load() != 0 {
		t.Fatalf("temp directory grant bypassed socket approval: cards=%d hits=%d", len(declinedSockCards), hits.Load())
	}

	// Step 2: Fresh session executes the clean two-step sequence (file -> socket) and observes 2 visible cards.
	s2 := host.replay(t)
	c1 := runTurnDeciding(t, s2, "two-step-1", "command", map[string]any{
		"command": "cat '" + confFile + "'",
	}, approveCard)
	if len(c1) != 1 || hits.Load() != 0 {
		t.Fatalf("two-step card 1: cards=%d hits=%d", len(c1), hits.Load())
	}
	c2 := runTurnDeciding(t, s2, "two-step-2", "command", map[string]any{
		"command": "curl -s --max-time 5 --unix-socket '" + sockPath + "' http://localhost/healthz",
	}, approveCard)
	if len(c2) != 1 || c2[0].ToolApproval.Plan.Subject.Kind != wire.ApprovalSubjectKindSocketSet {
		t.Fatalf("two-step card 2 = %+v, want 1 socket_set card", c2)
	}
	if s2.outcome(t, "two-step-2").Stdout != "daemon /healthz" || hits.Load() != 1 {
		t.Fatalf("two-step socket outcome = %+v hits=%d", s2.outcome(t, "two-step-2"), hits.Load())
	}
	var visibleCards int
	testutil.FailErr(t, "count two-step visible cards", s2.h.DB.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM checkpoints WHERE session_id = ? AND kind = 'tool_approval'`, s2.session,
	).Scan(&visibleCards))
	if visibleCards != 2 {
		t.Fatalf("two-step visible cards = %d, want 2", visibleCards)
	}
	if !slices.Contains(c1[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) {
		t.Fatalf("card 1 reasons = %v, want outside_roots_read", c1[0].ToolApproval.Plan.Reasons)
	}
}
