package wiring

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func outsideCommandFolder(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home directory", err)
	if hostHome := os.Getenv("PW_TEST_HOST_HOME"); hostHome != "" {
		home = hostHome
	}
	//nolint:usetesting // Must reside under the user home directory outside the project and system temp roots.
	folder, err := os.MkdirTemp(home, "paintedwolf-command-outside-*")
	testutil.FailErr(t, "outside folder", err)
	t.Cleanup(func() { _ = os.RemoveAll(folder) })
	return folder
}

func runCommandWithApprovals(t *testing.T, s *commandProgramSession, options ...string) []wire.CheckpointEvent {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.prompt(t.Context()) }()
	kind := wire.CheckpointKindToolApproval
	var checkpoints []wire.CheckpointEvent
	var runErr error
	testutil.WaitFor(t, func() bool {
		select {
		case runErr = <-done:
			return true
		default:
		}
		pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		if err != nil || len(pending) == 0 {
			return false
		}
		for _, checkpoint := range pending {
			if len(checkpoints) >= len(options) {
				t.Fatalf("unexpected approval card: %+v", checkpoint.ToolApproval.Plan)
			}
			checkpoints = append(checkpoints, checkpoint)
			option := options[len(checkpoints)-1]
			if option == "decline" {
				_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(s.h.OwnerCtx(t, t.Context()), s.session, checkpoint.ID, kind, &hitl.DecisionResult{Approved: false}, nil)
				testutil.FailErr(t, "decline approval", err)
				continue
			}
			if option == "recommended" {
				option = checkpoint.ToolApproval.Plan.RecommendedOptionID
			}
			if !slices.ContainsFunc(checkpoint.ToolApproval.Plan.Options, func(candidate wire.ApprovalOption) bool { return candidate.ID == option }) {
				t.Fatalf("approval option %q missing from plan: %+v", option, checkpoint.ToolApproval.Plan)
			}
			_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(s.h.OwnerCtx(t, t.Context()), s.session, checkpoint.ID, option)
			testutil.FailErr(t, "answer approval", err)
		}
		return false
	})
	testutil.FailErr(t, "complete command", runErr)
	if len(checkpoints) != len(options) {
		plans := make([]wire.ApprovalPlan, 0, len(checkpoints))
		for _, checkpoint := range checkpoints {
			plans = append(plans, checkpoint.ToolApproval.Plan)
		}
		outcome := s.outcome(t, "program-1")
		if outcome.Refusal != nil {
			t.Logf("refusal detail: %+v", *outcome.Refusal)
		}
		t.Fatalf("approval cards = %d, want %d; plans: %+v; outcome: %+v", len(checkpoints), len(options), plans, outcome)
	}
	return checkpoints
}

func TestNativeOutsideReadDeclineRetainsTypedRefusal(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "private.txt")
	testutil.FailErr(t, "seed private file", os.WriteFile(file, []byte("not delivered"), 0o600))
	s := newCommandProgramSession(t, "printf 'before\\n'; cat '"+file+"'", "")
	runCommandWithApprovals(t, s, "decline")
	outcome := s.outcome(t, "program-1")
	if outcome.Refusal == nil || outcome.Refusal.Code == "" || outcome.Stdout != "before\n" || outcome.Status == 0 {
		t.Fatalf("declined read lost its typed refusal or partial result: %+v", outcome)
	}
}

func TestNativeOutsideWriteUsesAdmittedFolder(t *testing.T) {
	outside := outsideCommandFolder(t)
	destination := filepath.Join(outside, "copied.txt")
	s := newCommandProgramSession(t, "cp notes.txt '"+destination+"'", "")
	cards := runCommandWithApprovals(t, s, "recommended")
	if !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsWrite) {
		t.Fatalf("outside write did not reach approval: %+v", cards[0].ToolApproval.Plan)
	}
	if got := s.outcome(t, "program-1"); got.Status != 0 {
		t.Fatalf("approved native write = %+v", got)
	}
	data, err := os.ReadFile(destination)
	testutil.FailErr(t, "read copied file", err)
	if string(data) != "first note\nsecond note\n" {
		t.Fatalf("copied bytes = %q", data)
	}
}

func TestNativeChildrenReceiveCommandPathCapabilityRequests(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "id_ed25519")
	testutil.FailErr(t, "seed capability input", os.WriteFile(file, []byte("capability bytes\n"), 0o600))
	for _, tc := range []struct {
		name, command, field, grant string
		want                        string
		subject                     wire.ApprovalSubjectKind
	}{
		{name: "read_path", command: "cat '" + file + "'", field: "read_path", grant: file, want: "capability bytes\n", subject: wire.ApprovalSubjectKindReadPathSet},
		{name: "write_root", command: "cp notes.txt '" + filepath.Join(outside, "output.txt") + "'", field: "write_root", grant: outside, subject: wire.ApprovalSubjectKindWriteRootSet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSessionWithArgs(t, map[string]any{
				"command":            tc.command,
				"capability_request": map[string]any{tc.field: tc.grant},
			}, wire.CreateSessionRequest{}, "")
			cards := runCommandWithApprovals(t, s, "recommended", "recommended")
			if !slices.ContainsFunc(cards, func(card wire.CheckpointEvent) bool {
				plan := card.ToolApproval.Plan
				return plan.Subject.Kind == tc.subject && slices.ContainsFunc(plan.Subject.Targets, func(target wire.ApprovalTarget) bool {
					return target.Label == tc.grant
				})
			}) {
				plans := make([]wire.ApprovalPlan, len(cards))
				for i, card := range cards {
					plans[i] = card.ToolApproval.Plan
				}
				t.Fatalf("%s was not reviewed by native child: %+v", tc.field, plans)
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 || got.Stdout != tc.want {
				t.Fatalf("%s native child = %+v", tc.field, got)
			}
		})
	}
}

func TestNativeCommandApprovalCardCounts(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			contained := newCommandProgramSession(t, "cat notes.txt", "", posture)
			runCommandWithApprovals(t, contained)
			if got := contained.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "first note\nsecond note\n" {
				t.Fatalf("contained read = %+v", got)
			}

			scratch := newCommandProgramSession(t, "cp notes.txt @scratch/copied.txt", "", posture)
			if posture == gate.PostureStrict {
				runCommandWithApprovals(t, scratch, "recommended")
			} else {
				runCommandWithApprovals(t, scratch)
			}
			if got := scratch.outcome(t, "program-1"); got.Status != 0 {
				t.Fatalf("scratch write = %+v", got)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "local service\n") }))
			t.Cleanup(server.Close)
			parsed, err := url.Parse(server.URL)
			testutil.FailErr(t, "parse local server", err)
			port, err := strconv.ParseUint(parsed.Port(), 10, 16)
			testutil.FailErr(t, "parse local port", err)

			granted := newCommandProgramSession(t, "curl -s '"+server.URL+"'", "", posture)
			if !granted.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(granted.session, []uint16{uint16(port)}, "granted-listener", "test-listener", nil) {
				t.Fatal("listener grant failed")
			}
			runCommandWithApprovals(t, granted)
			if got := granted.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "local service\n" {
				t.Fatalf("granted service = %+v", got)
			}

			foreign := newCommandProgramSession(t, "curl -s '"+server.URL+"'; curl -s '"+server.URL+"'", "", posture)
			switch posture {
			case gate.PostureStrict:
				runCommandWithApprovals(t, foreign, "recommended")
			case gate.PostureBalanced:
				runCommandWithApprovals(t, foreign, "recommended")
			default:
				runCommandWithApprovals(t, foreign)
			}
			if got := foreign.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "local service\nlocal service\n" {
				t.Fatalf("foreign service = %+v", got)
			}
		})
	}
}

func TestNativeOutsideReadUsesFolderApprovalAndAdmittedPathFrame(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		for _, choice := range []struct {
			name    string
			options []string
		}{
			{name: "once", options: []string{"approve_current_action", "approve_current_action"}},
			{name: "chat", options: []string{"recommended"}},
		} {
			t.Run(string(posture)+"/"+choice.name, func(t *testing.T) {
				outside := outsideCommandFolder(t)
				first, second := filepath.Join(outside, "first.txt"), filepath.Join(outside, "second.txt")
				testutil.FailErr(t, "seed first file", os.WriteFile(first, []byte("first outside\n"), 0o600))
				testutil.FailErr(t, "seed second file", os.WriteFile(second, []byte("second outside\n"), 0o600))
				s := newCommandProgramSession(t, "cat '"+first+"'; cat '"+second+"'", "", posture)
				cards := runCommandWithApprovals(t, s, choice.options...)
				plan := cards[0].ToolApproval.Plan
				face := slices.IndexFunc(plan.Options, func(option wire.ApprovalOption) bool { return option.ID == plan.RecommendedOptionID })
				if face < 0 || plan.Options[face].Rung != "chat" || !slices.Contains(plan.Reasons, wire.GateOutsideRootsRead) {
					t.Fatalf("outside read did not face the chat grant: %+v", cards[0].ToolApproval.Plan)
				}
				if len(cards[0].ToolApproval.Plan.DirectoryScopes) == 0 || cards[0].ToolApproval.Plan.DirectoryScopes[0] != outside {
					t.Fatalf("outside read was not scoped to its containing folder: %+v", cards[0].ToolApproval.Plan)
				}
				outcome := s.outcome(t, "program-1")
				if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "first outside\nsecond outside\n" {
					t.Fatalf("approved native reads were not delivered: %+v", outcome)
				}
			})
		}
	}
}

func TestNativePathBuiltinsUseAdmittedFolder(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "input.txt")
	testutil.FailErr(t, "seed path input", os.WriteFile(file, []byte("path input\n"), 0o600))
	for _, tc := range []struct{ name, command, want string }{
		{name: "realpath", command: "realpath '" + file + "'", want: file + "\n"},
		{name: "cd", command: "cd '" + outside + "'; cat input.txt", want: "path input\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSession(t, tc.command, "")
			cards := runCommandWithApprovals(t, s, "recommended")
			plan := cards[0].ToolApproval.Plan
			if !slices.Contains(plan.Reasons, wire.GateOutsideRootsRead) || len(plan.DirectoryScopes) == 0 || plan.DirectoryScopes[0] != outside {
				t.Fatalf("path builtin approved wrong scope: %+v", plan)
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 || got.Stdout != tc.want {
				t.Fatalf("path builtin outcome = %+v", got)
			}
		})
	}
}

func TestSedScriptFileProjectsOutsideEffects(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			outside := outsideCommandFolder(t)
			input := filepath.Join(outside, "input.txt")
			testutil.FailErr(t, "seed sed input", os.WriteFile(input, []byte("outside sed\n"), 0o600))
			s := newCommandProgramSession(t, "sed -f script.sed notes.txt", "", posture)
			testutil.FailErr(t, "seed sed script", os.WriteFile(filepath.Join(s.dir, "script.sed"), []byte("1r "+input+"\n"), 0o600))
			cards := runCommandWithApprovals(t, s, "recommended")
			if !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) {
				t.Fatalf("sed -f read was not projected: %+v", cards[0].ToolApproval.Plan)
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "first note\noutside sed\nsecond note\n" {
				t.Fatalf("sed -f result = %+v", got)
			}
			written := filepath.Join(outside, "written.txt")
			writer := newCommandProgramSession(t, "sed -f script.sed notes.txt", "", posture)
			testutil.FailErr(t, "seed sed write script", os.WriteFile(filepath.Join(writer.dir, "script.sed"), []byte("w "+written+"\n"), 0o600))
			writeCards := runCommandWithApprovals(t, writer, "recommended")
			if !slices.Contains(writeCards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsWrite) {
				t.Fatalf("sed -f write was not projected: %+v", writeCards[0].ToolApproval.Plan)
			}
			if got := writer.outcome(t, "program-1"); got.Status != 0 {
				t.Fatalf("sed -f write result = %+v", got)
			}
			data, err := os.ReadFile(written)
			testutil.FailErr(t, "read sed output", err)
			if string(data) != "first note\nsecond note\n" {
				t.Fatalf("sed -f write = %q", data)
			}
		})
	}
}

func TestSedOutsideScriptFileRequiresReadApproval(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			outside := outsideCommandFolder(t)
			script := filepath.Join(outside, "replace.sed")
			testutil.FailErr(t, "seed outside sed script", os.WriteFile(script, []byte("s/first/changed/\n"), 0o600))
			s := newCommandProgramSession(t, "sed -f '"+script+"' notes.txt", "", posture)
			cards := runCommandWithApprovals(t, s, "recommended")
			if len(cards) != 1 || !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) {
				t.Fatalf("outside sed script approval = %+v", cards)
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "changed note\nsecond note\n" {
				t.Fatalf("outside sed script result = %+v", got)
			}
		})
	}
}

func TestSedOutsideScriptDisclosesOutsideEffects(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			sourceFolder := outsideCommandFolder(t)
			effectFolder := outsideCommandFolder(t)
			input := filepath.Join(effectFolder, "input.txt")
			testutil.FailErr(t, "seed sed input", os.WriteFile(input, []byte("outside sed\n"), 0o600))
			script := filepath.Join(sourceFolder, "read.sed")
			testutil.FailErr(t, "seed outside sed script", os.WriteFile(script, []byte("1r "+input+"\n"), 0o600))
			s := newCommandProgramSession(t, "sed -f '"+script+"' notes.txt", "", posture)
			cards := runCommandWithApprovals(t, s, "recommended", "recommended")
			for i, card := range cards {
				if !slices.Contains(card.ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) {
					t.Fatalf("sed read card %d = %+v", i, card.ToolApproval.Plan)
				}
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "first note\noutside sed\nsecond note\n" {
				t.Fatalf("outside sed read result = %+v", got)
			}
		})
	}
}

func TestSedOutsideScriptWriteReceiptAfterStagedApproval(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			sourceFolder := outsideCommandFolder(t)
			writeFolder := outsideCommandFolder(t)
			written := filepath.Join(writeFolder, "written.txt")
			script := filepath.Join(sourceFolder, "write.sed")
			testutil.FailErr(t, "seed outside sed script", os.WriteFile(script, []byte("w "+written+"\n"), 0o600))
			s := newCommandProgramSession(t, "sed -f '"+script+"' notes.txt", "", posture)
			cards := runCommandWithApprovals(t, s, "recommended", "recommended")
			if !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) || !slices.Contains(cards[1].ToolApproval.Plan.Reasons, wire.GateOutsideRootsWrite) {
				t.Fatalf("staged sed cards = %+v", cards)
			}
			if got := s.outcome(t, "program-1"); got.Status != 0 {
				t.Fatalf("staged sed write result = %+v", got)
			}
			data, err := os.ReadFile(written)
			testutil.FailErr(t, "read staged sed output", err)
			if string(data) != "first note\nsecond note\n" {
				t.Fatalf("staged sed write = %q", data)
			}
			var operation, command, mode, certainty string
			var started, pins int
			err = s.h.DB.QueryRowContext(t.Context(), `SELECT e.operation_id,e.native_command,e.effect_mode,e.owner_started,e.effect_certainty,
				(SELECT count(*) FROM invocation_program_revision_pins pin WHERE pin.execution_id=e.id AND pin.path=?)
				FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id
				JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=? AND e.operation_id='sed'`, script, s.session).Scan(&operation, &command, &mode, &started, &certainty, &pins)
			testutil.FailErr(t, "read staged sed receipt", err)
			if operation != "sed" || command != "sed" || mode != "w-command" || started != 1 || certainty != "completed" || pins != 1 {
				t.Fatalf("staged sed receipt = %q %q %q started=%d certainty=%q pins=%d", operation, command, mode, started, certainty, pins)
			}
			declined := newCommandProgramSession(t, "sed -f '"+script+"' notes.txt", "", posture)
			runCommandWithApprovals(t, declined, "recommended", "decline")
			if got := declined.outcome(t, "program-1"); got.CompletedEffects != 0 || got.Status == 0 {
				t.Fatalf("declined staged sed effect = %+v", got)
			}
			err = declined.h.DB.QueryRowContext(t.Context(), `SELECT e.owner_started,e.effect_certainty,
				(SELECT count(*) FROM invocation_program_revision_pins pin WHERE pin.execution_id=e.id AND pin.path=?)
				FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id
				JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=? AND e.operation_id='sed'`, script, declined.session).Scan(&started, &certainty, &pins)
			testutil.FailErr(t, "read declined staged sed receipt", err)
			if started != 0 || certainty == "completed" || pins != 1 {
				t.Fatalf("declined staged sed receipt: started=%d certainty=%q pins=%d", started, certainty, pins)
			}
		})
	}
}

func TestYqExpressionAndFileApprovalCounts(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			expression := newCommandProgramSession(t, "printf 'x: 1\\n' | yq .x", "", posture)
			runCommandWithApprovals(t, expression)
			if got := expression.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "1\n" {
				t.Fatalf("expression result = %+v", got)
			}
			outside := outsideCommandFolder(t)
			file := filepath.Join(outside, "data.yaml")
			testutil.FailErr(t, "seed yq input", os.WriteFile(file, []byte("x: 2\n"), 0o600))
			ambiguous := newCommandProgramSession(t, "yq -n '"+file+"'", "", posture)
			runCommandWithApprovals(t, ambiguous)
			if got := ambiguous.outcome(t, "program-1"); got.Status == 0 {
				t.Fatalf("path-shaped expression unexpectedly read a file: %+v", got)
			}
			input := newCommandProgramSession(t, "yq .x '"+file+"'", "", posture)
			cards := runCommandWithApprovals(t, input, "recommended")
			if !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) {
				t.Fatalf("yq file was not projected: %+v", cards[0].ToolApproval.Plan)
			}
			if got := input.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "2\n" {
				t.Fatalf("file result = %+v", got)
			}
			fileOnly := newCommandProgramSession(t, "yq '"+file+"'", "", posture)
			fileCards := runCommandWithApprovals(t, fileOnly, "recommended")
			plan := fileCards[0].ToolApproval.Plan
			face := slices.IndexFunc(plan.Options, func(option wire.ApprovalOption) bool { return option.ID == plan.RecommendedOptionID })
			if face < 0 || plan.Options[face].Rung != "chat" || !slices.Contains(plan.Reasons, wire.GateOutsideRootsRead) || len(plan.DirectoryScopes) == 0 || plan.DirectoryScopes[0] != outside {
				t.Fatalf("file-only yq did not face a containing-folder Chat grant: %+v", plan)
			}
			if got := fileOnly.outcome(t, "program-1"); got.Status != 0 || got.Stdout != "x: 2\n" {
				t.Fatalf("file-only result = %+v", got)
			}
		})
	}
}
