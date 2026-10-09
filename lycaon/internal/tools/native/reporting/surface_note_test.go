package reporting_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSurfaceLedger struct {
	ev evidence.Ledger
}

func (s stubSurfaceLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return s.ev, nil
}

func (stubSurfaceLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

func surfaceNoteLedger(t *testing.T, path string) evidence.Ledger {
	t.Helper()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path, "offset": 1, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: fmt.Sprintf(`{"path":%q,"content":"1|package main","offset":1,"end_line":1,"limit":1}`, path),
		}},
	}
	return ledgertest.BuildFromMessages("", msgs)
}

func surfaceNoteSurveyLedger(t *testing.T, path string) evidence.Ledger {
	t.Helper()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: fmt.Sprintf(`{"path":%q,"mode":"outline","symbols":[{"kind":"func","name":"main","line":1}],"total_lines":500,"truncated":false,"selected":0,"total":1}`, path),
		}},
	}
	return ledgertest.BuildFromMessages("", msgs)
}

func TestSurfaceNoteGroundedEmit(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	var friction []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
		Friction: func(_ context.Context, _, code string) {
			friction = append(friction, code)
		},
	}))
	out := &tools.ToolInvocationOut{}
	raw, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Found the handler.",
		"cited_evidence": []any{"src/a.go:1"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Roots:     nil,
		Out:       out,
	})
	testutil.FailErr(t, "surface_note", err)
	var ack map[string]any
	testutil.FailErr(t, "decode ack", json.Unmarshal([]byte(raw), &ack))
	if ack["status"] != "noted" {
		t.Fatalf("status = %#v", ack["status"])
	}
	msgID, _ := ack["message_id"].(string)
	if msgID == "" || out.AgentNote == nil || out.AgentNote.MessageID != msgID {
		t.Fatalf("ack message_id=%q capture=%+v", msgID, out.AgentNote)
	}
	if out.AgentNote.Content != "Found the handler." {
		t.Fatalf("content = %q", out.AgentNote.Content)
	}
	if out.AgentNote.Grounding == nil || !out.AgentNote.Grounding.Traced {
		t.Fatalf("grounding = %+v want traced", out.AgentNote.Grounding)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v want none", friction)
	}
}

func TestSurfaceNoteRequiresExplicitCitation(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	var friction []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
		Friction: func(_ context.Context, _, code string) {
			friction = append(friction, code)
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary": "Ledger already observed this.",
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_UNGROUNDED" {
		t.Fatalf("err = %v want SURFACE_NOTE_UNGROUNDED", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked without explicit citation: %+v", out.AgentNote)
	}
	if len(friction) != 1 || friction[0] != "SURFACE_NOTE_UNGROUNDED" {
		t.Fatalf("friction = %v", friction)
	}
}

func TestSurfaceNoteRequiresToolOutput(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: surfaceNoteLedger(t, "src/a.go")},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Found the handler.",
		"cited_evidence": []any{"src/a.go:1"},
	}, tools.ToolContext{SessionID: "sess-1", Agent: orchestration.ProfileCoordinator})
	if err == nil || !strings.Contains(err.Error(), "tool output required") {
		t.Fatalf("err = %v want tool output required", err)
	}
}

func TestSurfaceNoteUngrounded(t *testing.T) {
	var friction []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: evidence.InitLedger()},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
		Friction: func(_ context.Context, _, code string) {
			friction = append(friction, code)
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary": "Nothing observed yet.",
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_UNGROUNDED" {
		t.Fatalf("err = %v want SURFACE_NOTE_UNGROUNDED", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked on reject: %+v", out.AgentNote)
	}
	if len(friction) != 1 || friction[0] != "SURFACE_NOTE_UNGROUNDED" {
		t.Fatalf("friction = %v", friction)
	}
}

func TestSurfaceNoteURLOnlyWithEmptyLedger(t *testing.T) {
	var friction []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: evidence.InitLedger()},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
		Friction: func(_ context.Context, _, code string) {
			friction = append(friction, code)
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":    "The upstream docs describe a different default.",
		"cited_urls": []any{"https://example.com/docs"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_URL_ONLY_UNGROUND" {
		t.Fatalf("err = %v want SURFACE_NOTE_URL_ONLY_UNGROUND", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked: %+v", out.AgentNote)
	}
	if len(friction) != 1 || friction[0] != "SURFACE_NOTE_URL_ONLY_UNGROUND" {
		t.Fatalf("friction = %v", friction)
	}
}

func TestSurfaceNoteHandleUngroundNamesOffender(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Line range and unseen ordinal.",
		"cited_evidence": []any{"src/a.go:37-50", "command#12"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       &tools.ToolInvocationOut{},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_HANDLE_UNGROUND" {
		t.Fatalf("err = %v want SURFACE_NOTE_HANDLE_UNGROUND", err)
	}
	sample, _ := reject.Data["offenders_sample"].(string)
	for _, want := range []string{"src/a.go:37-50", "command#12"} {
		if !strings.Contains(sample, want) {
			t.Fatalf("offenders_sample = %q want %q", sample, want)
		}
	}
	if count, _ := reject.Data["offender_count"].(int); count != 2 {
		t.Fatalf("offender_count = %#v want 2", reject.Data["offender_count"])
	}
	observed, _ := reject.Data["observed_paths_sample"].(string)
	if !strings.Contains(observed, "src/a.go") {
		t.Fatalf("observed_paths_sample = %q want the citable path", observed)
	}
}

func TestSurfaceNoteHandleMembershipIsChecked(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	for _, tc := range []struct {
		name         string
		handle       string
		wantErr      bool
		wantOffender string
	}{
		{"handle minted in this session", "read#1", false, ""},
		{"handle with an explicit line", "read#1:1", false, ""},
		{"ordinal beyond what was minted", "read#99", true, "read#99"},
		{"handle kind never observed", "command#12", true, "command#12"},
		{"invented kind with a line", "command#12:1", true, "command#12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
				Ledger: stubSurfaceLedger{ev: ev},
				Messages: func(context.Context, string) ([]api.Message, error) {
					return nil, nil
				},
			}))
			out := &tools.ToolInvocationOut{}
			_, err := reg.Run(context.Background(), "surface_note", map[string]any{
				"summary":        "Handle membership.",
				"cited_evidence": []any{tc.handle},
			}, tools.ToolContext{
				SessionID: "sess-1",
				Agent:     orchestration.ProfileCoordinator,
				Out:       out,
			})
			if !tc.wantErr {
				testutil.FailErr(t, "surface_note", err)
				if out.AgentNote == nil || out.AgentNote.Grounding == nil {
					t.Fatalf("no grounding attached for %q", tc.handle)
				}
				if len(out.AgentNote.Grounding.CitedEvidence) == 0 {
					t.Fatalf("grounding for %q carries no cited evidence", tc.handle)
				}
				return
			}
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_HANDLE_UNGROUND" {
				t.Fatalf("%q: err = %v want SURFACE_NOTE_HANDLE_UNGROUND", tc.handle, err)
			}
			if sample, _ := reject.Data["offenders_sample"].(string); !strings.Contains(sample, tc.wantOffender) {
				t.Fatalf("%q: offenders_sample = %q want %q", tc.handle, sample, tc.wantOffender)
			}
			if out.AgentNote != nil {
				t.Fatalf("capture leaked for %q", tc.handle)
			}
		})
	}
}

func TestSurfaceNoteHandleUngroundRendersOffender(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Unresolvable cite.",
		"cited_evidence": []any{"src/nope.go:7"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       &tools.ToolInvocationOut{},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("err = %v want ToolReject", err)
	}
	cfg, cfgErr := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", cfgErr)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	rendered, fmtErr := guidance.NewStaticRejectFormatter(cfg).Format(reject.Code, reject.Data)
	testutil.FailErr(t, "format "+reject.Code, fmtErr)
	for _, want := range []string{"src/nope.go:7", "src/a.go", "Keep cited_evidence"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered reject missing %q:\n%s", want, rendered)
		}
	}
	for _, leak := range []string{"{{", "{%"} {
		if strings.Contains(rendered, leak) {
			t.Fatalf("rendered reject leaks %q:\n%s", leak, rendered)
		}
	}
}

func TestSurfaceNoteHandleUnground(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	var friction []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
		Friction: func(_ context.Context, _, code string) {
			friction = append(friction, code)
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Bad cite.",
		"cited_evidence": []any{"src/missing.go:1"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_HANDLE_UNGROUND" {
		t.Fatalf("err = %v want SURFACE_NOTE_HANDLE_UNGROUND", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked: %+v", out.AgentNote)
	}
	if len(friction) != 1 || friction[0] != "SURFACE_NOTE_HANDLE_UNGROUND" {
		t.Fatalf("friction = %v", friction)
	}
}

func TestSurfaceNoteUnobservedURLIsStrict(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":    "Bad URL cite.",
		"cited_urls": []any{"https://unobserved.example/docs"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_HANDLE_UNGROUND" {
		t.Fatalf("err = %v want SURFACE_NOTE_HANDLE_UNGROUND", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked: %+v", out.AgentNote)
	}
}

func TestSurfaceNoteSurveyOnlyEvidenceIsStrict(t *testing.T) {
	ev := surfaceNoteSurveyLedger(t, "src/a.go")
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "Survey cite is too weak.",
		"cited_evidence": []any{"src/a.go"},
	}, tools.ToolContext{
		SessionID: "sess-1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_HANDLE_UNGROUND" {
		t.Fatalf("err = %v want SURFACE_NOTE_HANDLE_UNGROUND", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked: %+v", out.AgentNote)
	}
}

func TestSurfaceNoteRejectedInWorkerSession(t *testing.T) {
	ev := surfaceNoteLedger(t, "src/a.go")
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: ev},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "The migration touches a shared table.",
		"cited_evidence": []any{"src/a.go:1"},
	}, tools.ToolContext{
		SessionID:       "sess-1",
		ParentSessionID: "parent-1",
		Agent:           orchestration.ProfileImplementer,
		Out:             out,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_WORKER_SESSION" {
		t.Fatalf("err = %v want SURFACE_NOTE_WORKER_SESSION", err)
	}
	if out.AgentNote != nil {
		t.Fatalf("capture leaked from worker session: %+v", out.AgentNote)
	}
}

func TestSurfaceNoteInvalidArgs(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: evidence.InitLedger()},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return nil, nil
		},
	}))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "empty summary", args: map[string]any{"summary": "   "}},
		{name: "noncanonical string list", args: map[string]any{
			"summary":        "Found the handler.",
			"cited_evidence": []string{"src/a.go:1"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Run(context.Background(), "surface_note", tc.args, tools.ToolContext{
				SessionID: "sess-1",
				Agent:     orchestration.ProfileCoordinator,
				Out:       &tools.ToolInvocationOut{},
			})
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "SURFACE_NOTE_INVALID_ARGS" {
				t.Fatalf("err = %v want SURFACE_NOTE_INVALID_ARGS", err)
			}
		})
	}
}

func TestSurfaceNoteSourceAvoidsCloseoutEntry(t *testing.T) {
	data, err := os.ReadFile("surface_note.go")
	testutil.FailErr(t, "read surface_note.go", err)
	src := string(data)
	for _, banned := range []string{
		"CheckCloseout",
		"emitAssembledCloseout",
		"ProjectCoordinatorCloseoutProse",
	} {
		if strings.Contains(src, banned) {
			t.Fatalf("surface_note.go must not reference %s", banned)
		}
	}
}

func surfaceNoteHistoryWithStill() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleUser, Content: "show me the reloader UI"},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Visual: &api.VisualArtifact{
				ID: "art-still", Mime: "image/png", Source: api.VisualArtifactSourceCapture,
			},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Visual: &api.VisualArtifact{
				ID: "art-recording", Mime: "video/mp4", Source: api.VisualArtifactSourceCapture,
			},
		}},
	}
}

func surfaceNoteRegistryWithHistory(t *testing.T, history []api.Message) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterSurfaceNoteTool(reg, reporttools.SurfaceNoteDeps{
		Ledger: stubSurfaceLedger{ev: surfaceNoteLedger(t, "src/a.go")},
		Messages: func(context.Context, string) ([]api.Message, error) {
			return history, nil
		},
	}))
	return reg
}

func TestSurfaceNotePresentsStillFromThisTurn(t *testing.T) {
	reg := surfaceNoteRegistryWithHistory(t, surfaceNoteHistoryWithStill())
	out := &tools.ToolInvocationOut{}
	_, err := reg.Run(context.Background(), "surface_note", map[string]any{
		"summary":        "The status UI renders the reload events.",
		"cited_evidence": []any{"src/a.go:1"},
		"artifact_ids":   []any{"art-still"},
	}, tools.ToolContext{
		SessionID: "sess-present",
		Agent:     orchestration.ProfileCoordinator,
		Out:       out,
	})
	testutil.FailErr(t, "surface_note", err)
	if out.AgentNote == nil {
		t.Fatal("no note captured")
	}
	if got := out.AgentNote.ArtifactIDs; len(got) != 1 || got[0] != "art-still" {
		t.Fatalf("artifact_ids = %v, want [art-still]", got)
	}
}

func TestSurfaceNoteRejectsArtifactThisTurnNeverProduced(t *testing.T) {
	reg := surfaceNoteRegistryWithHistory(t, surfaceNoteHistoryWithStill())
	for _, id := range []string{"art-invented", "art-recording"} {
		out := &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), "surface_note", map[string]any{
			"summary":        "Here it is.",
			"cited_evidence": []any{"src/a.go:1"},
			"artifact_ids":   []any{id},
		}, tools.ToolContext{
			SessionID: "sess-present",
			Agent:     orchestration.ProfileCoordinator,
			Out:       out,
		})
		rej := &toolrejection.ToolReject{}
		if !errors.As(err, &rej) || rej.Code != "SURFACE_NOTE_ARTIFACT_UNKNOWN" {
			t.Fatalf("id %q: err=%v", id, err)
		}
		if out.AgentNote != nil {
			t.Fatalf("id %q: a rejected note must not emit", id)
		}
	}
}
