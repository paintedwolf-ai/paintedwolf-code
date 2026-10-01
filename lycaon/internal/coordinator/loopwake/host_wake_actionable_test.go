package loopwake_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func idleImplementSessionState() surface.ImplementSessionState {
	return surface.ImplementSessionState{PendingOverlayIDs: []string{}}
}

func busyImplementSessionState() surface.ImplementSessionState {
	return surface.ImplementSessionState{WorkersInFlight: 1, PendingOverlayIDs: []string{}}
}

func TestHostWakeActionableImplementerBatchIdle(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task agent_type="implementer" job_id="job-4" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-4",
			Status:   "complete",
		},
	}}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("expected actionable loop wake when implementer batch is idle")
	}
}

func TestHostWakeActionableImplementerMidBatchSkipped(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task agent_type="implementer" job_id="job-1" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1",
			Status:   "complete",
		},
	}}
	if loopwake.HostWakeActionableFromHistory(history, busyImplementSessionState()) {
		t.Fatal("expected skip when worker cycle is not idle")
	}
}

func TestHostWakeActionableRepoResearcherLoopWake(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task agent_type="` + orchestration.ProfileRepoResearcher + `" job_id="job-2" state="complete"><summary>paths</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{Status: "complete"},
	}}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("expected actionable loop wake after repo-researcher")
	}
}

func TestHostWakeActionableClosedBatchNotActionable(t *testing.T) {
	if loopwake.HostWakeActionableFromHistory(nil, surface.ImplementSessionState{
		BatchPhase: batch.PhaseClosed,
	}) {
		t.Fatal("closed batch must not be actionable for scheduled wake")
	}
}

func TestHostWakeActionablePendingOverlayPromote(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task agent_type="implementer" job_id="job-1" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-1", Status: "complete"},
	}}
	state := surface.ImplementSessionState{PendingOverlayIDs: []string{"job-pending"}}
	if !loopwake.HostWakeActionableFromHistory(history, state) {
		t.Fatal("expected actionable wake while ledger reports pending overlay promote")
	}
}

func TestHostWakeActionableNeedsDecisionMidBatch(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task agent_type="security-reviewer" job_id="job-dec" state="needs_decision"><summary>needs a decision</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-dec",
			Status:   api.WorkerSummaryStatusNeedsDecision,
		},
	}}
	if !loopwake.HostWakeActionableFromHistory(history, busyImplementSessionState()) {
		t.Fatal("needs_decision must be actionable while siblings are in flight")
	}
}

func TestHostWakeActionableIdleAfterCloseoutNotActionable(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit greenfield", Visibility: api.MessageVisibilityTranscript},
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task agent_type="repo-researcher" job_id="job-1" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-1", Status: "complete"},
		},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "Greenfield report…",
			Visibility: api.MessageVisibilityTranscript,
			Grounding:  &api.CitationGrounding{Traced: true, CitedEvidence: []api.CitationGroundingCitedEvidence{{Path: "AGENTS.md"}}},
		},
	}
	if loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("timer/loop wake after committed closeout with no newer worker must not be actionable")
	}
}

func TestHostWakeActionableNewWorkerAfterCloseoutActionable(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "another round", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "First report",
			Visibility: api.MessageVisibilityTranscript,
			Grounding:  &api.CitationGrounding{Traced: true},
		},
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task agent_type="repo-researcher" job_id="job-2" state="complete"><summary>more</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-2", Status: "complete"},
		},
	}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("expected actionable wake when a worker finished after the prior closeout")
	}
}

func TestHostWakeActionableTerminalWorkflowNotActionable(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task agent_type="implementer" job_id="job-1" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-1", Status: "complete"},
	}}
	fn := loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return idleImplementSessionState()
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return nil, nil
		},
	})
	if fn(context.Background(), loopwake.HostWakeActionableInput{SessionID: "sess"}) {
		t.Fatal("host-cycle must stop when the workflow run is gone")
	}
}

func TestHostWakeActionableTerminalWorkflowKeepsPendingOverlay(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task agent_type="implementer" job_id="job-1" state="partial" merge_status="pending"><summary>partial</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-1", Status: "partial"},
	}}
	fn := loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{PendingOverlayIDs: []string{"job-1"}}
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return nil, nil
		},
	})
	if !fn(context.Background(), loopwake.HostWakeActionableInput{SessionID: "sess"}) {
		t.Fatal("pending overlay must still wake after the run ends")
	}
}

func TestHostWakeActionableAgentNoteIsNotCloseout(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit greenfield", Visibility: api.MessageVisibilityTranscript},
		{
			Role:          api.MessageRoleAssistant,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-1", Status: "complete"},
		},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindAgentNote,
			Content:    "A verified milestone",
			Visibility: api.MessageVisibilityTranscript,
			Grounding:  &api.CitationGrounding{Traced: true},
		},
	}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("an agent note must not suppress the later synthesis wake")
	}
}

func TestHostWakeActionableGroundingAloneIsNotCloseout(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit greenfield", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Content:    "Grounded intermediate output",
			Visibility: api.MessageVisibilityTranscript,
			Grounding:  &api.CitationGrounding{Traced: true},
		},
	}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("grounding without completion-report state must not settle the intent")
	}
}

func TestHostWakeActionableCompletionKindSettlesWithoutInspectingGrounding(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "explain the repository", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "Repository overview",
			Visibility: api.MessageVisibilityTranscript,
		},
	}
	if loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("the completion-report kind, not optional grounding shape, settles the intent")
	}
}

func TestHostWakeActionableCommittedToolDraftIsNotCloseout(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix the tunnel", Visibility: api.MessageVisibilityTranscript},
		{
			Role:        api.MessageRoleAssistant,
			Kind:        api.MessageKindDraft,
			DraftStatus: api.DraftStatusCommitted,
			Visibility:  api.MessageVisibilityTranscript,
			ToolCalls:   []api.ToolCall{{ID: "call-ask", Name: "ask_user"}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    `{"status":"answered"}`,
			Visibility: api.MessageVisibilityTranscript,
		},
	}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("a retried ask_user step committed as a draft must not settle the intent")
	}
}

func TestHostWakeActionableToolStepAfterProseCloseoutActionable(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix the tunnel", Visibility: api.MessageVisibilityTranscript},
		{
			Role:        api.MessageRoleAssistant,
			Kind:        api.MessageKindDraft,
			DraftStatus: api.DraftStatusCommitted,
			Content:     "The tunnel is up.",
			Visibility:  api.MessageVisibilityTranscript,
		},
		{
			Role:        api.MessageRoleAssistant,
			DraftStatus: api.DraftStatusCommitted,
			Visibility:  api.MessageVisibilityTranscript,
			ToolCalls:   []api.ToolCall{{ID: "call-ask", Name: "ask_user"}},
		},
	}
	if !loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()) {
		t.Fatal("a tool step after a prose answer must reopen the intent")
	}
}

func TestHostWakeActionableObligationsOverrideStaleCloseout(t *testing.T) {
	// A committed closeout normally quiesces host wakes, but open workflow
	// obligations mean the run is not done — the wake stays actionable.
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "run the survey", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "Closeout report…",
			Visibility: api.MessageVisibilityTranscript,
			Grounding:  &api.CitationGrounding{Traced: true},
		},
	}
	fn := loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return idleImplementSessionState()
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning}, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
	})
	if !fn(context.Background(), loopwake.HostWakeActionableInput{SessionID: "sess"}) {
		t.Fatal("open workflow obligations must keep the host wake actionable past a stale closeout")
	}
}
