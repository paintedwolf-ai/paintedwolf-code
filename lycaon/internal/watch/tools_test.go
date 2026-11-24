package watch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/watch"
)

type watchToolsHarness struct {
	reg   *tools.DefaultRegistry
	store *watch.Store
	now   time.Time
	root  string
}

func newWatchToolsHarness(t *testing.T, standingExists map[string]bool) *watchToolsHarness {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "watch-tools.db")
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)
	now := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	store := watch.NewStore(sqlDB, clock)

	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(t, "NewCatalogRegistry", err)

	rootDir := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(rootDir, ".git"), 0o755))

	testutil.FailErr(t, "RegisterWatchTools", watch.RegisterWatchTools(reg, watch.ToolDeps{
		Store: store,
		ProfileConditions: map[string]map[string]bool{
			"coordinator": {
				"next_worker_done": true, "all_workers_idle": true, "overlay_promote_pending": true,
				"worker_decision_needed": true, "worker_failed": true, "scan_done": true,
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
			"implement": {
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
			"web_research": {
				"http_ready": true, "port_ready": true, "timer": true,
			},
		},
		WorkerKnown:  func(_, h string) bool { return h == "reviewer" || h == "implementer" },
		ProcessKnown: func(_, h string) bool { return strings.HasPrefix(h, "cmd-") },
		ProjectChat:  func(_ context.Context, projectID string) bool { return projectID == testdbseed.DefaultProjectID },
		StandingWatchExists: func(_, id string) bool {
			return standingExists[id]
		},
	}))
	return &watchToolsHarness{reg: reg, store: store, now: now, root: rootDir}
}

func (h *watchToolsHarness) coordCtx(callID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{
			ProjectID:  testdbseed.DefaultProjectID,
			SessionID:  "sess-1",
			ToolCallID: callID,
			Agent:      "coordinator",
		},
		Source: tools.InvocationSource{
			ActiveRootID: "repo",
			Roots:        []projectroot.RootRef{{ID: "repo", Label: "repo", Path: h.root, IsPrimary: true}},
		},
		Local: tools.InvocationLocal{
			LoopbackConnectGranted: true,
			LoopbackConnectPorts:   []uint16{8080},
		},
	}
}

func (h *watchToolsHarness) call(t *testing.T, name string, args map[string]any, tctx tools.ToolContext) (map[string]any, *toolrejection.ToolReject) {
	t.Helper()
	def, ok := h.reg.Definition(name)
	if !ok {
		t.Fatalf("missing tool %s", name)
	}
	res, err := def.Handler(t.Context(), args, tctx)
	if err != nil {
		var rej *toolrejection.ToolReject
		if errors.As(err, &rej) {
			return nil, rej
		}
		t.Fatalf("unexpected non-reject error from %s: %v", name, err)
	}
	var decoded map[string]any
	testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(res), &decoded))
	return decoded, nil
}

func TestWatchAndWatchControlCanonicalLifecycleAndStandingDraft(t *testing.T) {
	existingStanding := "11111111-2222-4333-8444-555555555555"
	h := newWatchToolsHarness(t, map[string]bool{existingStanding: true})

	receipt, rej := h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-17"}}},
	}, h.coordCtx("inv-1"))
	if rej != nil {
		t.Fatalf("unexpected reject: %+v", rej)
	}
	if receipt["handle"] != "watch-1" || receipt["state"] != "pending" || receipt["delivery"] != "collect" || receipt["destination"] != "session" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	wc, _ := receipt["wait_condition"].(map[string]any)
	handles, _ := wc["handles"].([]any)
	if wc["kind"] != "watch_match" || len(handles) != 1 || handles[0] != "watch-1" {
		t.Fatalf("wait_condition = %+v, want watch_match [watch-1]", wc)
	}
	if _, hasDraft := receipt["standing_draft"]; hasDraft {
		t.Fatalf("ordinary watch should omit standing_draft: %+v", receipt)
	}

	// Inspect does not consume matches; set_delivery updates to wake; cancel is idempotent.
	inspected, rej := h.call(t, "watch_control", map[string]any{"handle": "watch-1", "operation": "inspect"}, h.coordCtx("inv-2"))
	if rej != nil || inspected["state"] != "live" || inspected["delivery"] != "collect" {
		t.Fatalf("inspect = %+v, rej = %+v", inspected, rej)
	}
	updated, rej := h.call(t, "watch_control", map[string]any{"handle": "watch-1", "operation": "set_delivery", "delivery": "wake"}, h.coordCtx("inv-3"))
	if rej != nil || updated["delivery"] != "wake" {
		t.Fatalf("set_delivery = %+v, rej = %+v", updated, rej)
	}
	canceled1, rej := h.call(t, "watch_control", map[string]any{"handle": "watch-1", "operation": "cancel"}, h.coordCtx("inv-4"))
	if rej != nil || canceled1["outcome"] != "canceled" || canceled1["receipt_id"] == "" {
		t.Fatalf("cancel 1 = %+v, rej = %+v", canceled1, rej)
	}
	canceled2, rej := h.call(t, "watch_control", map[string]any{"handle": "watch-1", "operation": "cancel"}, h.coordCtx("inv-5"))
	if rej != nil || canceled2["receipt_id"] != canceled1["receipt_id"] {
		t.Fatalf("cancel 2 = %+v, want idempotent receipt_id %v", canceled2, canceled1["receipt_id"])
	}

	// Standing draft forces delivery=collect and returns standing_draft=true + empty overlapping_standing slice.
	draftReceipt, rej := h.call(t, "watch", map[string]any{
		"conditions": []any{
			map[string]any{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"},
			map[string]any{
				"kind": "timer",
				"calendar": map[string]any{
					"days":  []any{"weekdays"},
					"times": []any{"09:00"},
				},
				"time_zone": "America/Los_Angeles",
			},
		},
		"repeat": true,
		"standing": map[string]any{
			"name":     "Morning main check",
			"prompt":   "Summarize overnight landings on main.",
			"replaces": existingStanding,
		},
	}, h.coordCtx("inv-6"))
	if rej != nil {
		t.Fatalf("standing draft rejected: %+v", rej)
	}
	if draftReceipt["standing_draft"] != true || draftReceipt["delivery"] != "collect" {
		t.Fatalf("draftReceipt = %+v", draftReceipt)
	}
	overlaps, ok := draftReceipt["overlapping_standing"].([]any)
	if !ok || len(overlaps) != 0 {
		t.Fatalf("overlapping_standing = %#v, want empty JSON array", draftReceipt["overlapping_standing"])
	}
}

func TestWatchAndWatchControlRejectCodesAndOARFeedback(t *testing.T) {
	h := newWatchToolsHarness(t, nil)

	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "LoadEffectivePolicy", err)

	assertOARRule := func(t *testing.T, toolName string, profile string, rej *toolrejection.ToolReject, wantCode string) {
		t.Helper()
		if rej == nil || rej.Code != wantCode {
			t.Fatalf("got reject %+v, want code %s", rej, wantCode)
		}
		data := map[string]any{"tool": toolName, "profile": profile, "rejection_code": rej.Code}
		for k, v := range rej.Data {
			data[k] = v
		}
		facts := oarcopy.FactsFromData(data)
		matched := false
		for _, rule := range rs.All() {
			if rule.ID == wantCode {
				matched = true
				for _, reqFact := range rule.RequiresFacts {
					if _, present := facts[reqFact]; !present {
						t.Fatalf("rule %s missing required fact %s in %+v", wantCode, reqFact, facts)
					}
				}
			}
		}
		if !matched {
			t.Fatalf("OAR policy catalog missing rule %s", wantCode)
		}
	}

	// 1. WAIT_CONDITION_NOT_ALLOWED on worker attempting coordinator condition.
	workerCtx := h.coordCtx("err-1")
	workerCtx.Identity.Agent = "implement"
	workerCtx.Identity.WorkerJobID = "job-1"
	_, rej := h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "next_worker_done"}},
	}, workerCtx)
	assertOARRule(t, "watch", "implement", rej, "WAIT_CONDITION_NOT_ALLOWED")

	// 2. WATCH_CONDITION_INVALID (watch_match passed to watch, or bad git_ref).
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "watch_match", "handles": []any{"watch-1"}}},
	}, h.coordCtx("err-2a"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_CONDITION_INVALID")

	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "git_ref", "root": "repo", "ref": "main"}},
	}, h.coordCtx("err-2b"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_CONDITION_INVALID")

	// 3. WATCH_REPEAT_UNSUPPORTED (terminal process_done with handles).
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		"repeat":     true,
	}, h.coordCtx("err-3"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_REPEAT_UNSUPPORTED")

	// 4. WATCH_WAKE_NOT_ALLOWED (worker requesting wake, and repeating timer requesting wake).
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		"delivery":   "wake",
	}, workerCtx)
	assertOARRule(t, "watch", "implement", rej, "WATCH_WAKE_NOT_ALLOWED")

	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
		"repeat":     true,
		"delivery":   "wake",
	}, h.coordCtx("err-4b"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_WAKE_NOT_ALLOWED")

	// 5. WATCH_EXPIRY_INVALID (< 1m ahead).
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 5000}},
		"expires_at": h.now.Add(30 * time.Second).Format(time.RFC3339),
	}, h.coordCtx("err-5"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_EXPIRY_INVALID")

	// 6. WATCH_HANDLE_UNKNOWN.
	_, rej = h.call(t, "watch_control", map[string]any{
		"handle":    "watch-999",
		"operation": "inspect",
	}, h.coordCtx("err-6"))
	assertOARRule(t, "watch_control", "coordinator", rej, "WATCH_HANDLE_UNKNOWN")

	// 7. WATCH_TERMINAL (set_delivery after cancel).
	created, rej := h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 5000}},
	}, h.coordCtx("term-create"))
	if rej != nil {
		t.Fatalf("create failed: %+v", rej)
	}
	handle := created["handle"].(string)
	_, _ = h.call(t, "watch_control", map[string]any{"handle": handle, "operation": "cancel"}, h.coordCtx("term-cancel"))
	_, rej = h.call(t, "watch_control", map[string]any{
		"handle":    handle,
		"operation": "set_delivery",
		"delivery":  "wake",
	}, h.coordCtx("err-7"))
	assertOARRule(t, "watch_control", "coordinator", rej, "WATCH_TERMINAL")

	// 8. WATCH_DRAFT_INVALID (disallowed condition kind and outside project chat).
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		"standing":   map[string]any{"name": "Bad", "prompt": "Run checks"},
	}, h.coordCtx("err-8a"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_DRAFT_INVALID")

	draftChatCtx := h.coordCtx("err-8b")
	draftChatCtx.Identity.ProjectID = "draft-project-id"
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
		"standing":   map[string]any{"name": "No project", "prompt": "Run checks"},
	}, draftChatCtx)
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_DRAFT_INVALID")

	// 9. WATCH_CAPACITY_REACHED (8 live explicit watches).
	for i := range watch.MaxLiveExplicitWatches {
		_, r := h.call(t, "watch", map[string]any{
			"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 10000}},
		}, h.coordCtx("cap-"+string(rune('a'+i))))
		if r != nil {
			t.Fatalf("register %d failed: %+v", i, r)
		}
	}
	_, rej = h.call(t, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 10000}},
	}, h.coordCtx("err-9"))
	assertOARRule(t, "watch", "coordinator", rej, "WATCH_CAPACITY_REACHED")
}
