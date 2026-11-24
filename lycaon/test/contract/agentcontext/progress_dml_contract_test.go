package contract

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

var bannedPackProgressTokens = []string{
	"reconciliation_token",
	"item_ids",
	"## Progress",
	"- [ ]",
	"- [x]",
	"- [~]",
	"- [>]",
	"PROGRESS_NESTED_LINE",
	"PROGRESS_TOO_MANY_LINES",
	"PROGRESS_REVISION_REQUIRED",
	"PROGRESS_REVISION_CHANGED",
	"PROGRESS_RECONCILIATION_CHANGED",
	"PROGRESS_DOCUMENT_TOO_LARGE",
}

func scanPackProgressContent(rel, body string) []string {
	var violations []string
	for _, token := range bannedPackProgressTokens {
		if strings.Contains(body, token) {
			violations = append(violations, rel+": contains retired progress token "+token)
		}
	}
	isProgressContext := strings.Contains(body, "update_progress") || strings.Contains(rel, "PROGRESS_") || strings.Contains(rel, "progress")
	if isProgressContext && strings.Contains(body, "expected_revision") {
		violations = append(violations, rel+": progress context references expected_revision")
	}
	return violations
}

func TestPackProgressCatalogUsesRowDMLOnly(t *testing.T) {
	seeded := scanPackProgressContent(
		"platform/shared/units/seeded-progress.md",
		"Call update_progress with ## Progress, - [ ] item, expected_revision, item_ids, and reconciliation_token.",
	)
	if len(seeded) < 5 {
		t.Fatalf("seeded scanner missed retired progress tokens: %v", seeded)
	}

	packRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf")
	exts := map[string]struct{}{".md": {}, ".yaml": {}}
	var violations []string
	err := contractcheck.WalkFiles(packRoot, exts, false, func(path string, data []byte) error {
		rel, relErr := filepath.Rel(packRoot, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if rel == "platform/host/anchors/oar-profile.yaml" {
			// oar-profile.yaml declares expected_revision for COMMAND_REVISION_CONFLICT.
			for _, token := range bannedPackProgressTokens {
				if strings.Contains(string(data), token) {
					violations = append(violations, rel+": contains retired progress token "+token)
				}
			}
			return nil
		}
		violations = append(violations, scanPackProgressContent(rel, string(data))...)
		return nil
	})
	contractcheck.FailErr(t, "walk pack progress catalog", err)
	contractcheck.FailViolations(t, "pack progress catalog contains legacy Markdown-CAS syntax", violations)
}

type stubAckReconciler struct {
	unaccounted map[string]progress.UnaccountedResult
}

func (s *stubAckReconciler) Reconciliation(_ string) *progress.Reconciliation {
	list := s.list()
	if len(list) == 0 {
		return nil
	}
	return &progress.Reconciliation{Unaccounted: list}
}

func (s *stubAckReconciler) UnaccountedResults(_ string) []progress.UnaccountedResult {
	return s.list()
}

func (s *stubAckReconciler) AcknowledgeTx(_ context.Context, _ *sql.Tx, _, _ string, handles []string) (progress.AckOutcome, error) {
	return progress.AckOutcome{AccountedJobIDs: handles}, nil
}

func (s *stubAckReconciler) Release(_ string, jobIDs []string) {
	for _, h := range jobIDs {
		delete(s.unaccounted, h)
	}
}

func (s *stubAckReconciler) list() []progress.UnaccountedResult {
	out := make([]progress.UnaccountedResult, 0, len(s.unaccounted))
	for _, k := range []string{"res-scout-1", "res-impl-1"} {
		if u, ok := s.unaccounted[k]; ok {
			out = append(out, u)
		}
	}
	return out
}

func estimateTokens(b int) int { return (b + 3) / 4 }

func TestProgressCanonicalTrajectoryTokenReduction(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	var dmlSchemaBytes int
	for _, meta := range reg.List() {
		if meta.Name == "update_progress" {
			wire, err := json.Marshal(toolSurfaceWireView{
				Name:        meta.Name,
				Description: meta.Description,
				Parameters:  meta.ArgsSchema,
			})
			contractcheck.FailErr(t, "marshal update_progress schema", err)
			dmlSchemaBytes = len(wire)
			break
		}
	}
	if dmlSchemaBytes == 0 {
		t.Fatal("update_progress schema not found in boot registry")
	}

	labels := []string{
		"Audit session progress SQLite schema and queries",
		"Implement host-minted monotonic row IDs across rewind",
		"Unify row edits and result acknowledgement in one transaction",
		"Rewrite coordinator prompts, kicks, and OAR policies",
		"Migrate benchmark trajectories and evaluation suites",
		"Verify full repository contract and security gates",
	}
	extraLabel := "Add regression test for held program envelope basis"

	store := progresstest.New(t, "root-1")
	reconciler := &stubAckReconciler{unaccounted: map[string]progress.UnaccountedResult{}}
	runDML := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, reconciler)

	dmlTurns := []struct {
		before func()
		args   map[string]any
	}{
		{
			args: map[string]any{
				"replace": []any{
					map[string]any{"label": labels[0]},
					map[string]any{"label": labels[1]},
					map[string]any{"label": labels[2]},
					map[string]any{"label": labels[3]},
					map[string]any{"label": labels[4]},
					map[string]any{"label": labels[5]},
				},
			},
		},
		{
			before: func() {
				reconciler.unaccounted["res-scout-1"] = progress.UnaccountedResult{
					Handle: "res-scout-1", Worker: "repo-researcher", Summary: "Mapped 14 progress callers.",
				}
			},
			args: map[string]any{"acknowledge_results": []any{"res-scout-1"}},
		},
		{
			before: func() {
				reconciler.unaccounted["res-impl-1"] = progress.UnaccountedResult{
					Handle: "res-impl-1", Worker: "implementer", Summary: "Landed relational schema.",
				}
			},
			args: map[string]any{
				"update":              []any{map[string]any{"id": "p1", "state": "done"}},
				"acknowledge_results": []any{"res-impl-1"},
			},
		},
		{
			args: map[string]any{
				"update": []any{
					map[string]any{"id": "p2", "state": "done"},
					map[string]any{"id": "p3", "state": "done"},
				},
				"add": []any{map[string]any{"label": extraLabel, "after_id": "p3"}},
			},
		},
		{
			args: map[string]any{
				"update": []any{
					map[string]any{"id": "p4", "state": "done"},
					map[string]any{"id": "p5", "state": "done"},
					map[string]any{"id": "p6", "state": "na"},
					map[string]any{"id": "p7", "state": "done"},
				},
			},
		},
	}

	var dmlSnapshotBytes, dmlCallBytes, dmlResultBytes int
	ctx := context.Background()
	for i, turn := range dmlTurns {
		if turn.before != nil {
			turn.before()
		}
		snap, err := store.Snapshot(ctx, "root-1")
		contractcheck.FailErr(t, "snapshot turn", err)
		snap.Reconciliation = reconciler.Reconciliation("root-1")
		snapJSON, err := json.Marshal(progress.ForRequest("root-1", snap))
		contractcheck.FailErr(t, "marshal snapshot", err)
		dmlSnapshotBytes += len(snapJSON)

		callRaw, err := json.Marshal(turn.args)
		contractcheck.FailErr(t, "marshal dml call", err)
		dmlCallBytes += len(callRaw)

		resJSON, err := runDML(ctx, turn.args, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "root-1"},
		})
		contractcheck.FailErr(t, "execute dml turn", err)
		if strings.TrimSpace(resJSON) == "" {
			t.Fatalf("turn %d returned empty result", i+1)
		}
		dmlResultBytes += len(resJSON)
	}

	// Baseline Markdown-CAS schema and 5-turn trajectory over identical labels and worker settlements.
	casSchemaRaw, err := json.Marshal(map[string]any{
		"name":        "update_progress",
		"description": "Write or update the session progress checklist as Markdown with optimistic concurrency and positional item_ids.",
		"parameters": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"content"},
			"properties": map[string]any{
				"content":              map[string]any{"type": "string", "description": "Complete Markdown checklist starting with ## Progress."},
				"expected_revision":    map[string]any{"type": "string", "description": "64-character hex revision from the latest snapshot."},
				"item_ids":             map[string]any{"type": "array", "items": map[string]any{"type": []string{"string", "null"}}},
				"reconciliation_token": map[string]any{"type": "string", "description": "64-character hex token acknowledging settled worker rounds."},
			},
		},
	})
	contractcheck.FailErr(t, "marshal baseline schema", err)
	casSchemaBytes := len(casSchemaRaw)

	uuids := []string{
		"550e8400-e29b-41d4-a716-446655440001",
		"550e8400-e29b-41d4-a716-446655440002",
		"550e8400-e29b-41d4-a716-446655440003",
		"550e8400-e29b-41d4-a716-446655440004",
		"550e8400-e29b-41d4-a716-446655440005",
		"550e8400-e29b-41d4-a716-446655440006",
		"550e8400-e29b-41d4-a716-446655440007",
	}
	hex64 := strings.Repeat("a1b2c3d4", 8)
	tok64 := strings.Repeat("f0e1d2c3", 8)

	mdTurn := func(states []string, ls []string) string {
		var b strings.Builder
		b.WriteString("## Progress\n")
		for i, st := range states {
			b.WriteString("- [" + st + "] " + ls[i] + "\n")
		}
		return b.String()
	}
	md1 := mdTurn([]string{" ", " ", " ", " ", " ", " "}, labels)
	md3 := mdTurn([]string{"x", " ", " ", " ", " ", " "}, labels)
	labels7 := []string{labels[0], labels[1], labels[2], extraLabel, labels[3], labels[4], labels[5]}
	md4 := mdTurn([]string{"x", "x", "x", " ", " ", " ", " "}, labels7)
	md5 := mdTurn([]string{"x", "x", "x", "x", "x", "x", "~"}, labels7)

	casSnap := func(md string, ids []string, recon bool) int {
		if md == "" {
			return 0
		}
		m := map[string]any{"content": md, "revision": hex64, "item_ids": ids}
		if recon {
			m["reconciliation"] = map[string]any{"token": tok64, "jobs": []string{"job-550e8400-e29b-41d4-a716-446655440099"}}
		}
		raw, _ := json.Marshal(m)
		return len(raw)
	}
	casCall := func(md, rev string, ids []any, tok string) int {
		m := map[string]any{"content": md}
		if rev != "" {
			m["expected_revision"] = rev
		}
		if ids != nil {
			m["item_ids"] = ids
		}
		if tok != "" {
			m["reconciliation_token"] = tok
		}
		raw, _ := json.Marshal(m)
		return len(raw)
	}
	casRes := func(md string, ids []string) int {
		raw, _ := json.Marshal(map[string]any{
			"status":   "updated",
			"revision": hex64,
			"item_ids": ids,
			"snapshot": map[string]any{
				"content":  md,
				"revision": hex64,
				"item_ids": ids,
				"summary":  map[string]any{"pending": 3, "done": 3, "na": 0, "optional": 0},
			},
		})
		return len(raw)
	}

	toAny := func(ss []string) []any {
		out := make([]any, len(ss))
		for i, s := range ss {
			out[i] = s
		}
		return out
	}

	casSnapshotBytes := casSnap("", nil, false) +
		casSnap(md1, uuids[:6], true) +
		casSnap(md1, uuids[:6], true) +
		casSnap(md3, uuids[:6], false) +
		casSnap(md4, uuids[:7], false)
	casCallBytes := casCall(md1, "", nil, "") +
		casCall(md1, hex64, toAny(uuids[:6]), tok64) +
		casCall(md3, hex64, toAny(uuids[:6]), tok64) +
		casCall(md4, hex64, []any{uuids[0], uuids[1], uuids[2], nil, uuids[3], uuids[4], uuids[5]}, "") +
		casCall(md5, hex64, toAny(uuids[:7]), "")
	casResultBytes := casRes(md1, uuids[:6]) +
		casRes(md1, uuids[:6]) +
		casRes(md3, uuids[:6]) +
		casRes(md4, uuids[:7]) +
		casRes(md5, uuids[:7])

	dmlTotalTokens := estimateTokens(dmlSchemaBytes + dmlSnapshotBytes + dmlCallBytes + dmlResultBytes)
	casTotalTokens := estimateTokens(casSchemaBytes + casSnapshotBytes + casCallBytes + casResultBytes)

	t.Logf("trajectory token comparison (5 turns, 6->7 deliverables): DML=%d tokens (schema=%d, snapshots=%d, calls=%d, results=%d) vs CAS=%d tokens (schema=%d, snapshots=%d, calls=%d, results=%d)",
		dmlTotalTokens, estimateTokens(dmlSchemaBytes), estimateTokens(dmlSnapshotBytes), estimateTokens(dmlCallBytes), estimateTokens(dmlResultBytes),
		casTotalTokens, estimateTokens(casSchemaBytes), estimateTokens(casSnapshotBytes), estimateTokens(casCallBytes), estimateTokens(casResultBytes))

	if dmlTotalTokens >= casTotalTokens {
		t.Fatalf("relational DML did not reduce cumulative trajectory tokens: DML=%d >= CAS=%d", dmlTotalTokens, casTotalTokens)
	}
	if estimateTokens(dmlCallBytes) >= estimateTokens(casCallBytes)/2 {
		t.Fatalf("relational DML call payloads were not <50%% of CAS call payloads: DML=%d CAS=%d", estimateTokens(dmlCallBytes), estimateTokens(casCallBytes))
	}
}
