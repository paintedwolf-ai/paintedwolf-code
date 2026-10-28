package conformance_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
	"github.com/lycaon/lycaon/test/wiring"
)

// runtimeObservation is everything one case left through the command entry.
type runtimeObservation struct {
	Outcome        command.ProgramOutcome
	TopLevelReject string
	Files          map[string]*commandconformance.File
	ChildReceipts  []string
	NativeGaps     []toolcontract.NativeGap
	ApprovalPlans  []wire.ApprovalPlan
	// Roots are the absolute spellings of the case's workspace.
	Roots []string
}

// streams is the observation as the corpus compares it with a reference.
func (o runtimeObservation) streams() commandconformance.Observation {
	return commandconformance.Observation{
		Status: o.Outcome.Status,
		Stdout: []byte(o.Outcome.Stdout),
		Stderr: []byte(o.Outcome.Stderr),
		Files:  o.Files,
	}.Normalize(o.Roots...)
}

type conformanceRunner struct {
	h     *wiring.Harness
	seed  commandconformance.GitSeed
	model *caseModel
}

func loadGitSeed(t *testing.T) commandconformance.GitSeed {
	t.Helper()
	seed, err := commandconformance.LoadGitSeed()
	testutil.FailErr(t, "load the Git seed", err)
	return seed
}

// newConformanceRunner runs case programs through the assembled command entry.
func newConformanceRunner(t *testing.T, posture gate.Posture, neverAsk bool) *conformanceRunner {
	t.Helper()
	model := &caseModel{}

	h := wiring.BuildForTest(t, wiring.WithLLMClient(model))
	testutil.FailErr(t, "set approval posture", h.Server.Admin.Settings.Service.Approvals.PutGlobal(settings.ApprovalConfig{
		Posture:  posture,
		NeverAsk: &neverAsk,
	}))
	m, err := h.Workflows.Manager.Resolver.ForSession(t.Context(), "", "", "implement", "1.0.0")
	testutil.FailErr(t, "resolve implement manifest", err)
	m.Controls.ContentReview = nil
	h.RegisterManifest(m)
	return &conformanceRunner{h: h, seed: loadGitSeed(t), model: model}
}

// seedWorkspace writes the case's workspace the way the reference driver seeds
// it: the same entries and permissions, every entry last modified at the seed time.
func (r *conformanceRunner) seedWorkspace(t *testing.T, c commandconformance.Case) string {
	t.Helper()
	dir := t.TempDir()
	entries := c.Workspace(r.seed)
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry := entries[name]
		target := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "create parents of "+name, os.MkdirAll(filepath.Dir(target), 0o755))
		var err error
		switch entry.Mode.Type() {
		case fs.ModeDir:
			err = os.MkdirAll(target, permissions(entry.Mode, 0o755))
		case fs.ModeSymlink:
			err = os.Symlink(entry.Link, target)
		default:
			err = os.WriteFile(target, entry.Data, permissions(entry.Mode, 0o644))
		}
		testutil.FailErr(t, "seed "+name, err)
	}
	testutil.FailErr(t, "set seed times", filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type() == fs.ModeSymlink {
			return err
		}
		return os.Chtimes(path, referenceuserland.SeedTime, referenceuserland.SeedTime)
	}))
	return dir
}

func permissions(mode, fallback fs.FileMode) fs.FileMode {
	if mode.Perm() == 0 {
		return fallback
	}
	return mode.Perm()
}

func (r *conformanceRunner) runCase(t *testing.T, c commandconformance.Case) runtimeObservation {
	return r.runCaseWithRecommendedApprovals(t, c, 0)
}

// runCaseWithRecommendedApprovals answers each approval card with its recommended
// option, as a person would, and fails unless exactly expectedCards appear.
func (r *conformanceRunner) runCaseWithRecommendedApprovals(t *testing.T, c commandconformance.Case, expectedCards int) runtimeObservation {
	t.Helper()
	r.model.use(t, c)
	dir := r.seedWorkspace(t, c)
	projectID := uuid.NewString()
	testdbseed.InsertProjectRoot(t, r.h.DB, projectID, dir)
	sess, err := r.h.Store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: projectID}, projectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set implementer agent", r.h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	r.h.SeedProgress(t, t.Context(), sess.ID)
	var plans []wire.ApprovalPlan
	if expectedCards == 0 {
		_, err = r.h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "run case "+c.ID)
		testutil.FailErr(t, "prompt conformance case", err)
	} else {
		plans = r.promptAnsweringApprovals(t, projectID, sess.ID, c.ID, expectedCards)
	}

	receipts := r.queryChildReceipts(t, sess.ID)
	obs := runtimeObservation{
		Files:         trackedFiles(t, dir, c.Tracked),
		ChildReceipts: receipts.Children,
		NativeGaps:    receipts.Gaps,
		ApprovalPlans: plans,
		Roots:         workspaceRoots(dir),
	}
	content := r.toolResult(t, sess.ID, "call-"+c.ID)
	if strings.Contains(content, `"program_id"`) {
		dec := json.NewDecoder(strings.NewReader(content[strings.Index(content, "{"):]))
		testutil.FailErr(t, "decode program outcome", dec.Decode(&obs.Outcome))
	} else {
		obs.TopLevelReject = content
	}
	return obs
}

// promptAnsweringApprovals answers each approval card the host announces, in the
// order it announces them, until the turn ends. The test deadline bounds a turn
// that never ends.
func (r *conformanceRunner) promptAnsweringApprovals(t *testing.T, projectID, sessionID, caseID string, expectedCards int) []wire.ApprovalPlan {
	t.Helper()
	stream, unsubscribe, err := r.h.Events.Subscribe(t.Context(), events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to checkpoints", err)
	defer unsubscribe()
	done := make(chan error, 1)
	go func() {
		_, promptErr := r.h.Sessions.Manager.Submissions.Prompt(t.Context(), sessionID, "run case "+caseID)
		done <- promptErr
	}()
	var plans []wire.ApprovalPlan
	answered := map[string]bool{}
	for {
		select {
		case promptErr := <-done:
			testutil.FailErr(t, "prompt conformance case", promptErr)
			if len(plans) != expectedCards {
				t.Fatalf("approval cards = %d, want %d: %+v", len(plans), expectedCards, plans)
			}
			return plans
		case env := <-stream:
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			raw, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode checkpoint event", err)
			var checkpoint wire.CheckpointEvent
			testutil.FailErr(t, "decode checkpoint event", json.Unmarshal(raw, &checkpoint))
			if checkpoint.SessionID != sessionID || checkpoint.Status != wire.CheckpointStatusPending || answered[checkpoint.ID] {
				continue
			}
			if checkpoint.Kind != wire.CheckpointKindToolApproval || checkpoint.ToolApproval == nil || len(plans) >= expectedCards {
				t.Fatalf("unexpected checkpoint: %+v", checkpoint)
			}
			plan := checkpoint.ToolApproval.Plan
			if !slices.ContainsFunc(plan.Options, func(option wire.ApprovalOption) bool { return option.ID == plan.RecommendedOptionID }) {
				t.Fatalf("recommended option missing from plan: %+v", plan)
			}
			answered[checkpoint.ID] = true
			plans = append(plans, plan)
			_, resolveErr := r.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(r.h.OwnerCtx(t, t.Context()), sessionID, checkpoint.ID, plan.RecommendedOptionID)
			testutil.FailErr(t, "answer recommended approval", resolveErr)
		}
	}
}

func (r *conformanceRunner) toolResult(t *testing.T, sessionID, callID string) string {
	t.Helper()
	msgs, err := r.h.Store.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "read session messages", err)
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role == wire.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.ToolCallID == callID && msg.ToolResult.Content != "" {
			return msg.ToolResult.Content
		}
	}
	t.Fatalf("call %s produced no tool output in %d messages", callID, len(msgs))
	return ""
}

// workspaceRoots are the spellings of dir a program can print: as created, and
// with symbolic links resolved.
func workspaceRoots(dir string) []string {
	roots := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		roots = append(roots, resolved)
	}
	return roots
}

func trackedFiles(t *testing.T, dir string, tracked []string) map[string]*commandconformance.File {
	t.Helper()
	if len(tracked) == 0 {
		return nil
	}
	out := make(map[string]*commandconformance.File, len(tracked))
	for _, rel := range tracked {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			out[rel] = nil
			continue
		}
		testutil.FailErr(t, "inspect "+rel, err)
		f := &commandconformance.File{Mode: commandconformance.Mode(info.Mode() & (fs.ModeType | fs.ModePerm))}
		switch {
		case info.Mode().Type() == fs.ModeSymlink:
			f.Link, err = os.Readlink(target)
		case info.Mode().IsRegular():
			f.Data, err = os.ReadFile(target)
		}
		testutil.FailErr(t, "read "+rel, err)
		out[rel] = f
	}
	return out
}

type caseReceipts struct {
	Children []string
	Gaps     []toolcontract.NativeGap
}

func (r *conformanceRunner) queryChildReceipts(t *testing.T, sessionID string) caseReceipts {
	t.Helper()
	rows, err := r.h.DB.QueryContext(t.Context(), `SELECT e.operation_id, e.state, e.native_gap_json FROM invocation_program_executions e
		JOIN invocation_programs p ON p.id = e.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND e.operation_id NOT LIKE 'program %' ORDER BY e.ordinal`, sessionID)
	testutil.FailErr(t, "query child receipts", err)
	defer rows.Close()
	var out caseReceipts
	for rows.Next() {
		var op, state, gap string
		testutil.FailErr(t, "scan child receipt", rows.Scan(&op, &state, &gap))
		out.Children = append(out.Children, op+"="+state)
		if gap != "" {
			var fact toolcontract.NativeGap
			testutil.FailErr(t, "decode native gap receipt", json.Unmarshal([]byte(gap), &fact))
			out.Gaps = append(out.Gaps, fact)
		}
	}
	testutil.FailErr(t, "iterate child receipts", rows.Err())
	return out
}
