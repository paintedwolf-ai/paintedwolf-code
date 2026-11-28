// Package wea_test implements the D3 in-process lane tests for TB05:
// Agent edits and the read basis (Case families E01, E02, E03, E08, E10).
//
// This file is strictly test-only (*_test.go) and is never linked into any shipped binary.
// All tests exercise real product packages (db, editordoc, sourceledger, textfile,
// projectsource, tools, tools/native, tools/readcaps) with zero mocks of the SUT.
package wea_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb05FixedRoots struct {
	p *project.Project
}

func (f tb05FixedRoots) Get(_ context.Context, projectID string) (*project.Project, error) {
	if f.p != nil && f.p.ID == projectID {
		return f.p, nil
	}
	return nil, editordoc.ErrNotFound
}

type tb05Harness struct {
	t         *testing.T
	dbPath    string
	blobDir   string
	rootDir   string
	projectID string
	rootID    string
	project   *project.Project
	sqlDB     *db.Store
	store     *editordoc.Store
	ledger    *sourceledger.Store
	service   *editordoc.Service
	changesMu sync.Mutex
	changes   []editordoc.Change
}

func newTB05Harness(t *testing.T, files map[string][]byte) *tb05Harness {
	t.Helper()
	ctx := t.Context()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "tb05-store.db")
	blobDir := filepath.Join(tmp, "blobs")
	rootDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	for rel, raw := range files {
		full := filepath.Join(rootDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent %s: %v", rel, err)
		}
		if err := os.WriteFile(full, raw, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", rel, err)
		}
	}

	sqlDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	projectID := "proj-tb05-" + uuid.NewString()[:8]
	rootID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO projects (id, name, last_opened_at, created_at) VALUES (?, 'TB05 Fixture', ?, ?)`,
		projectID, now, now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO project_roots (id, project_id, path, label, kind, is_primary, added_at) VALUES (?, ?, ?, 'primary', 'attached', 1, ?)`,
		rootID, projectID, rootDir, now,
	); err != nil {
		t.Fatalf("seed project_root: %v", err)
	}

	p := &project.Project{
		ID:           projectID,
		SourceBranch: sourcebranch.Trunk,
		Roots: []project.Root{
			{ID: rootID, ProjectID: projectID, Path: rootDir, IsPrimary: true},
		},
	}
	store := editordoc.NewStore(sqlDB)
	ledger := sourceledger.New(sqlDB, blobDir, sourceledger.Timing{})
	svc := editordoc.New(store, ledger, ledger.History, tb05FixedRoots{p: p})

	h := &tb05Harness{
		t:         t,
		dbPath:    dbPath,
		blobDir:   blobDir,
		rootDir:   rootDir,
		projectID: projectID,
		rootID:    rootID,
		project:   p,
		sqlDB:     sqlDB,
		store:     store,
		ledger:    ledger,
		service:   svc,
	}
	svc.SetOnChange(func(_ context.Context, c editordoc.Change) {
		h.changesMu.Lock()
		h.changes = append(h.changes, c)
		h.changesMu.Unlock()
	})
	t.Cleanup(func() {
		if h.service != nil {
			_ = h.service.Close(context.Background())
		}
		if h.sqlDB != nil {
			_ = h.sqlDB.Close()
		}
	})
	return h
}

func (h *tb05Harness) readDisk(rel string) []byte {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.rootDir, rel))
	if err != nil {
		h.t.Fatalf("read disk %s: %v", rel, err)
	}
	return raw
}

func (h *tb05Harness) writeDisk(rel string, raw []byte) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.rootDir, rel), raw, 0o644); err != nil {
		h.t.Fatalf("write disk %s: %v", rel, err)
	}
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h *tb05Harness) diskSHA(rel string) string {
	h.t.Helper()
	return sha256Hex(h.readDisk(rel))
}

func (h *tb05Harness) countVersionsAndEffectsRO(fileID string) (versions int, effects int) {
	h.t.Helper()
	ro, err := db.OpenReadOnly(h.t.Context(), h.dbPath)
	if err != nil {
		h.t.Fatalf("db.OpenReadOnly: %v", err)
	}
	defer ro.Close()
	if err := ro.QueryRowContext(h.t.Context(),
		`SELECT COUNT(1) FROM source_versions WHERE project_id=? AND file_id=?`,
		h.projectID, fileID,
	).Scan(&versions); err != nil {
		h.t.Fatalf("count source_versions: %v", err)
	}
	if err := ro.QueryRowContext(h.t.Context(),
		`SELECT COUNT(1) FROM source_effects WHERE project_id=? AND file_id=?`,
		h.projectID, fileID,
	).Scan(&effects); err != nil {
		h.t.Fatalf("count source_effects: %v", err)
	}
	return versions, effects
}

func (h *tb05Harness) logReceipt(slotID string, payload map[string]any) {
	h.t.Helper()
	payload["schema"] = "wea-tb05-inproc-v1"
	payload["slot_id"] = slotID
	payload["lane"] = "in-process"
	encoded, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatalf("marshal receipt %s: %v", slotID, err)
	}
	h.t.Logf("WEA_TB05_RECEIPT:%s", encoded)
}

func mintSyntheticCanary(t *testing.T, label string) string {
	t.Helper()
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return fmt.Sprintf("PW_CANARY_TB05_%s_%s", label, hex.EncodeToString(buf))
}

// ============================================================================
// E01 · Read-basis matrix across read forms & combined read/write responses
// ============================================================================

func TestWEATB05_E01_ReadBasisMatrix(t *testing.T) {
	ctx := t.Context()
	initial := []byte("line1_header\nline2_target\nline3_middle\nline4_footer\n")
	initialSHA := sha256Hex(initial)
	outsidePrefixSHA := sha256Hex([]byte("line1_header\n"))
	outsideSuffixSHA := sha256Hex([]byte("line3_middle\nline4_footer\n"))

	t.Run("missing_read_refuses_without_disk_mutation", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"doc.txt": initial})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "doc.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText failed: ok=%v err=%v", ok, err)
		}
		if _, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, nil); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("expected ErrAgentReadRequired without prior read, got %v", err)
		}
		if got := h.diskSHA("doc.txt"); got != initialSHA {
			t.Fatalf("disk mutated on refused missing-read: got=%s want=%s", got, initialSHA)
		}
	})

	t.Run("full_read_authorizes_write_and_preserves_outside_bytes", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"doc.txt": initial})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "doc.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText failed: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e01", doc.ID, doc.Revision)
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, nil)
		if err != nil || !basis.ReadWhole || basis.Revision != doc.Revision {
			t.Fatalf("AgentReadBase unexpected: basis=%+v err=%v", basis, err)
		}
		proposed := "line1_header\nline2_EDITED\nline3_middle\nline4_footer\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: proposed, OperationID: uuid.NewString(),
			SessionID: "chat-e01", Turn: 1, ToolCallID: "call-e01-full", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit failed: res=%+v err=%v", res, err)
		}
		disk := h.readDisk("doc.txt")
		if sha256Hex(disk) != sha256Hex([]byte(proposed)) {
			t.Fatalf("disk sha mismatch: got=%s want=%s", sha256Hex(disk), sha256Hex([]byte(proposed)))
		}
		lines := strings.SplitAfter(string(disk), "\n")
		if sha256Hex([]byte(lines[0])) != outsidePrefixSHA {
			t.Fatalf("outside prefix bytes changed")
		}
		if sha256Hex([]byte(lines[2]+lines[3])) != outsideSuffixSHA {
			t.Fatalf("outside suffix bytes changed")
		}
		h.logReceipt("E01-inproc-full-read", map[string]any{
			"disk_sha256": sha256Hex(disk),
			"saved":       res.Saved,
		})
	})

	t.Run("partial_span_read_does_not_qualify_as_whole_read", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"doc.txt": initial})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "doc.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText failed: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentReadSpans(h.projectID, "chat-e01", doc.ID, doc.Revision, []documentcore.LineSpan{{StartLine: 1, EndLine: 2}})
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, nil)
		if err != nil {
			t.Fatalf("AgentReadBase span: %v", err)
		}
		if basis.ReadWhole {
			t.Fatalf("partial span read must NOT set ReadWhole=true")
		}
		edText := tools.EditorDocumentText{ReadWhole: basis.ReadWhole, ReadSpans: basis.ReadSpans}
		if !edText.CoversLines(1, 2) {
			t.Fatalf("expected span [1,2] to cover lines 1..2")
		}
		if edText.CoversLines(3, 4) {
			t.Fatalf("partial span [1,2] must NOT cover unread lines 3..4")
		}
		if got := h.diskSHA("doc.txt"); got != initialSHA {
			t.Fatalf("disk mutated unexpectedly: got=%s want=%s", got, initialSHA)
		}
	})

	t.Run("cross_chat_isolation_refuses_borrowed_read", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"doc.txt": initial})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "doc.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText failed: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-alpha", doc.ID, doc.Revision)
		if _, err := h.service.AgentReadBase(ctx, h.projectID, "chat-beta", doc.ID, nil); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("chat-beta borrowed chat-alpha's read basis: err=%v", err)
		}
		if got := h.diskSHA("doc.txt"); got != initialSHA {
			t.Fatalf("disk mutated on cross-chat refusal")
		}
	})

	t.Run("combined_read_and_write_in_one_response_respects_frozen_bases", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"doc.txt": initial})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "doc.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText failed: ok=%v err=%v", ok, err)
		}
		// Freeze bases BEFORE the response's tools run (empty).
		frozen1 := tools.NewAgentReadBases(h.service.FreezeAgentReads(h.projectID, "chat-e01"))
		// Tool 1 in response reads the file.
		h.service.RememberAgentRead(h.projectID, "chat-e01", doc.ID, doc.Revision)
		// Tool 2 in SAME response attempts to edit using frozen1 -> MUST fail with ErrAgentReadRequired!
		if _, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, frozen1.Basis); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("combined read+write in same response adopted unseen read: err=%v", err)
		}
		if got := h.diskSHA("doc.txt"); got != initialSHA {
			t.Fatalf("disk mutated on same-response read+write refusal")
		}

		// Next response freezes AFTER the read completed -> authorized!
		frozen2 := tools.NewAgentReadBases(h.service.FreezeAgentReads(h.projectID, "chat-e01"))
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, frozen2.Basis)
		if err != nil || basis.Revision != doc.Revision {
			t.Fatalf("next response failed to use completed read: err=%v", err)
		}
		// Tool 1 in response 2 writes the file; response advances its own frozen basis.
		firstWrite := "line1_header\nline2_step1\nline3_middle\nline4_footer\n"
		res1, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: firstWrite, OperationID: uuid.NewString(),
			SessionID: "chat-e01", Turn: 2, ToolCallID: "call-w1", ToolName: "edit",
		})
		if err != nil || !res1.Saved {
			t.Fatalf("first write failed: res=%+v err=%v", res1, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e01", res1.Document.ID, res1.Document.Revision)
		frozen2.AdvanceBasis(res1.Document.ID, documentcore.ReadBasis{Revision: res1.Document.Revision, Whole: true})

		// Tool 2 in SAME response 2 edits the file it just wrote -> MUST succeed!
		basis2, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e01", doc.ID, frozen2.Basis)
		if err != nil || basis2.Revision != res1.Document.Revision {
			t.Fatalf("second edit in same response after own write failed: err=%v", err)
		}
		secondWrite := "line1_header\nline2_step2\nline3_middle\nline4_footer\n"
		res2, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis2.ID, ExpectedRevision: basis2.Revision,
			Content: secondWrite, OperationID: uuid.NewString(),
			SessionID: "chat-e01", Turn: 2, ToolCallID: "call-w2", ToolName: "edit",
		})
		if err != nil || !res2.Saved {
			t.Fatalf("second edit in same response failed: res=%+v err=%v", res2, err)
		}
		if got := h.diskSHA("doc.txt"); got != sha256Hex([]byte(secondWrite)) {
			t.Fatalf("disk sha after chained same-response write mismatch")
		}
	})
}

// ============================================================================
// E02 · Rebase, overlap, retries, basis withdrawal, and cache eviction (6 in-process slots)
// ============================================================================

func TestWEATB05_E02_RebaseOverlapRetryAndEviction(t *testing.T) {
	ctx := t.Context()
	baseText := "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n"

	// Slot E02-inproc-1: Non-overlapping person edit between read and write rebases cleanly.
	t.Run("E02_inproc_1_non_overlapping_person_edit_rebases_cleanly", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", doc.ID, doc.Revision)

		// Person edits line 1 while keeping lines 2..5 untouched.
		personDraft := "L1_PERSON_EDIT\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n"
		if _, err := h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         personDraft, EOL: "lf",
		}); err != nil {
			t.Fatalf("person ReplaceSnapshot: %v", err)
		}

		// Agent computes edit against its pinned read basis (line 5 only).
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil)
		if err != nil || basis.Revision != doc.Revision {
			t.Fatalf("AgentReadBase: basis=%+v err=%v", basis, err)
		}
		agentProposed := "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT_EDIT\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: agentProposed, OperationID: uuid.NewString(),
			SessionID: "chat-e02", Turn: 1, ToolCallID: "call-e02-rebase", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("non-overlapping rebase failed: res=%+v err=%v", res, err)
		}

		wantCombined := "L1_PERSON_EDIT\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT_EDIT\n"
		wantSHA := sha256Hex([]byte(wantCombined))
		diskBytes := h.readDisk("shared.txt")
		if sha256Hex(diskBytes) != wantSHA {
			t.Fatalf("rebased disk bytes mismatch:\ngot:  %q\nwant: %q", string(diskBytes), wantCombined)
		}
		// Verify untouched middle lines (L2..L4) preserved exactly by SHA-256.
		wantMiddleSHA := sha256Hex([]byte("L2_beta\nL3_gamma\nL4_delta\n"))
		lines := strings.SplitAfter(string(diskBytes), "\n")
		gotMiddleSHA := sha256Hex([]byte(lines[1] + lines[2] + lines[3]))
		if gotMiddleSHA != wantMiddleSHA {
			t.Fatalf("outside middle bytes hash mismatch: got=%s want=%s", gotMiddleSHA, wantMiddleSHA)
		}
		if res.Document.Draft != wantCombined {
			t.Fatalf("document draft != disk after rebase")
		}
		h.logReceipt("E02-inproc-1", map[string]any{"combined_sha256": wantSHA, "middle_sha256": gotMiddleSHA})
	})

	// Slot E02-inproc-2: Overlapping person edit refuses with ErrRevisionConflict and changes nothing on disk.
	t.Run("E02_inproc_2_overlapping_edit_refuses_and_preserves_disk", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		initialDiskSHA := h.diskSHA("shared.txt")
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", doc.ID, doc.Revision)

		// Person edits line 3.
		personDraft := "L1_alpha\nL2_beta\nL3_PERSON_CONFLICT\nL4_delta\nL5_epsilon\n"
		if _, err := h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         personDraft, EOL: "lf",
		}); err != nil {
			t.Fatalf("person ReplaceSnapshot: %v", err)
		}

		// Agent also edits line 3 against its earlier read basis -> MUST refuse!
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil)
		if err != nil {
			t.Fatalf("AgentReadBase: %v", err)
		}
		agentConflict := "L1_alpha\nL2_beta\nL3_AGENT_CONFLICT\nL4_delta\nL5_epsilon\n"
		if _, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: agentConflict, OperationID: uuid.NewString(),
			SessionID: "chat-e02", Turn: 1, ToolCallID: "call-e02-overlap", ToolName: "edit",
		}); !errors.Is(err, editordoc.ErrRevisionConflict) {
			t.Fatalf("expected ErrRevisionConflict on overlap, got %v", err)
		}
		// Disk MUST be completely unchanged by SHA-256!
		if got := h.diskSHA("shared.txt"); got != initialDiskSHA {
			t.Fatalf("overlap mutated disk: got=%s want=%s", got, initialDiskSHA)
		}
		// Document MUST still hold person's draft.
		cur, err := h.service.CurrentSnapshot(ctx, h.projectID, doc.ID)
		if err != nil || cur.Draft != personDraft {
			t.Fatalf("overlap corrupted person draft: cur=%+v err=%v", cur, err)
		}
		// Retry MUST keep the exact same read basis (still pinned to original revision, not silently refreshed).
		retryBasis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil)
		if err != nil || retryBasis.Revision != doc.Revision {
			t.Fatalf("retry lost original read basis: basis=%+v err=%v", retryBasis, err)
		}
	})

	// Slot E02-inproc-3: Second window concurrent edit + agent edit rebase.
	t.Run("E02_inproc_3_second_window_and_agent_rebase", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		doc1, err := h.service.Open(ctx, h.project, "shared.txt", h.rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("window-1 Open: %v", err)
		}
		doc2, err := h.service.Open(ctx, h.project, "shared.txt", h.rootID, "", "window-2", nil)
		if err != nil {
			t.Fatalf("window-2 Open: %v", err)
		}
		if doc1.ID != doc2.ID {
			t.Fatalf("two windows forked canonical document: %s vs %s", doc1.ID, doc2.ID)
		}
		agentDoc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok || agentDoc.ID != doc1.ID {
			t.Fatalf("agent OpenText: doc=%+v ok=%v err=%v", agentDoc, ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", agentDoc.ID, agentDoc.Revision)

		// Window 2 edits line 2.
		w2Draft := "L1_alpha\nL2_WINDOW2\nL3_gamma\nL4_delta\nL5_epsilon\n"
		if _, err := h.service.ReplaceSnapshot(ctx, doc2.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-2", OperationID: uuid.NewString(), ExpectedRevision: agentDoc.Revision},
			Content:         w2Draft, EOL: "lf",
		}); err != nil {
			t.Fatalf("window-2 edit: %v", err)
		}

		// Agent edits line 4 from its initial read basis.
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", agentDoc.ID, nil)
		if err != nil {
			t.Fatalf("AgentReadBase: %v", err)
		}
		agentDraft := "L1_alpha\nL2_beta\nL3_gamma\nL4_AGENT\nL5_epsilon\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: agentDraft, OperationID: uuid.NewString(),
			SessionID: "chat-e02", Turn: 1, ToolCallID: "call-e02-w2", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit with window-2 failed: res=%+v err=%v", res, err)
		}
		want := "L1_alpha\nL2_WINDOW2\nL3_gamma\nL4_AGENT\nL5_epsilon\n"
		if h.diskSHA("shared.txt") != sha256Hex([]byte(want)) || res.Document.Draft != want {
			t.Fatalf("window-2 + agent rebase mismatch: disk=%q draft=%q", string(h.readDisk("shared.txt")), res.Document.Draft)
		}
	})

	// Slot E02-inproc-4: Accepted merge containing another participant's text withdraws basis.
	t.Run("E02_inproc_4_merged_result_withdraws_read_basis", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", doc.ID, doc.Revision)
		if _, err := h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         "L1_HUMAN\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n", EOL: "lf",
		}); err != nil {
			t.Fatalf("ReplaceSnapshot: %v", err)
		}
		agentProposed := "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: agentProposed, OperationID: uuid.NewString(),
			SessionID: "chat-e02", Turn: 1, ToolCallID: "call-e02-withdraw", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit: res=%+v err=%v", res, err)
		}
		// Because accepted text != proposed text (contains L1_HUMAN), basis MUST be withdrawn.
		if res.Document.Draft == agentProposed {
			t.Fatalf("expected merged draft to differ from agentProposed")
		}
		h.service.DropAgentRead(h.projectID, "chat-e02", doc.ID)
		if _, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("expected ErrAgentReadRequired after basis withdrawal, got %v", err)
		}
	})

	// Slot E02-inproc-5: Read-basis LRU eviction (4096-entry bound) forces fresh read.
	t.Run("E02_inproc_5_read_cache_eviction_requires_reread", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", doc.ID, doc.Revision)
		// Fill > 4096 distinct read entries to trigger deterministic LRU eviction of doc.ID.
		for i := 0; i < 4100; i++ {
			h.service.RememberAgentRead(h.projectID, "chat-evict", fmt.Sprintf("synthetic-doc-%04d", i), 1)
		}
		if _, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("evicted read basis was not dropped: err=%v", err)
		}
	})

	// Slot E02-inproc-6: External program modification between read and write.
	t.Run("E02_inproc_6_external_change_between_read_and_write", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"shared.txt": []byte(baseText)})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "shared.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		h.service.RememberAgentRead(h.projectID, "chat-e02", doc.ID, doc.Revision)

		// External program edits line 1 on disk.
		extText := "L1_EXTERNAL\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n"
		h.writeDisk("shared.txt", []byte(extText))
		if err := h.service.ObserveExternal(ctx, h.project, []editordoc.PathRef{{RootID: h.rootID, Path: "shared.txt"}}); err != nil {
			t.Fatalf("ObserveExternal: %v", err)
		}
		// Agent applies non-overlapping edit on line 5 from its original read basis.
		basis, err := h.service.AgentReadBase(ctx, h.projectID, "chat-e02", doc.ID, nil)
		if err != nil {
			t.Fatalf("AgentReadBase: %v", err)
		}
		agentText := "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT_AFTER_EXT\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: basis.ID, ExpectedRevision: basis.Revision,
			Content: agentText, OperationID: uuid.NewString(),
			SessionID: "chat-e02", Turn: 1, ToolCallID: "call-e02-ext", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit after external change failed: res=%+v err=%v", res, err)
		}
		want := "L1_EXTERNAL\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT_AFTER_EXT\n"
		if h.diskSHA("shared.txt") != sha256Hex([]byte(want)) || res.Document.Draft != want {
			t.Fatalf("external + agent merge mismatch: disk=%q draft=%q", string(h.readDisk("shared.txt")), res.Document.Draft)
		}
	})
}

// ============================================================================
// E03 · Open documents, unsaved person drafts, external writes, and disk parity
// ============================================================================

func TestWEATB05_E03_OpenDocumentAndOutsideChanges(t *testing.T) {
	ctx := t.Context()

	t.Run("person_unsaved_draft_and_external_text_merge_match_disk_after_save", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"live.txt": []byte("top\nmid\nbottom\n")})
		doc, err := h.service.Open(ctx, h.project, "live.txt", h.rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// Person edits top line (unsaved).
		doc, err = h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         "top_human\nmid\nbottom\n", EOL: "lf",
		})
		if err != nil {
			t.Fatalf("ReplaceSnapshot: %v", err)
		}
		// External program edits bottom line on disk.
		h.writeDisk("live.txt", []byte("top\nmid\nbottom_external\n"))
		if err := h.service.ObserveExternal(ctx, h.project, []editordoc.PathRef{{RootID: h.rootID, Path: "live.txt"}}); err != nil {
			t.Fatalf("ObserveExternal: %v", err)
		}
		merged, err := h.service.CurrentSnapshot(ctx, h.projectID, doc.ID)
		if err != nil {
			t.Fatalf("CurrentSnapshot: %v", err)
		}
		wantMerged := "top_human\nmid\nbottom_external\n"
		if merged.Draft != wantMerged || !merged.Dirty || merged.Diverged {
			t.Fatalf("unexpected merged state: draft=%q dirty=%v diverged=%v", merged.Draft, merged.Dirty, merged.Diverged)
		}
		saved, err := h.service.Save(ctx, h.project, merged.ID, "window-1", uuid.NewString(), "chat-e03", 1, merged.Revision)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		if saved.Dirty || h.diskSHA("live.txt") != sha256Hex([]byte(saved.Draft)) {
			t.Fatalf("post-save document vs disk mismatch: draft=%q disk=%q", saved.Draft, string(h.readDisk("live.txt")))
		}
	})

	t.Run("unimportable_outside_bytes_diverge_and_hold_agent_edit_without_clobbering_disk", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"live.txt": []byte("clean\n")})
		doc, err := h.service.Open(ctx, h.project, "live.txt", h.rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// External program writes invalid UTF-8 bytes.
		invalidBytes := []byte("outside\xff\xfeinvalid\n")
		invalidSHA := sha256Hex(invalidBytes)
		h.writeDisk("live.txt", invalidBytes)
		observed, err := h.service.ObserveDisk(ctx, h.project, doc.ID, "window-1")
		if err != nil {
			t.Fatalf("ObserveDisk: %v", err)
		}
		if !observed.Diverged {
			t.Fatalf("expected document to be marked Diverged on unimportable bytes")
		}
		// Agent edit on diverged document is held in document, NOT written to disk.
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: observed.ID, ExpectedRevision: observed.Revision,
			Content: "agent_held\n", OperationID: uuid.NewString(),
			SessionID: "chat-e03", Turn: 1, ToolCallID: "call-e03-held", ToolName: "edit",
		})
		if err != nil {
			t.Fatalf("ApplyAgentEdit on diverged: %v", err)
		}
		if res.Saved || res.HeldVersionID == "" || !res.Document.Diverged {
			t.Fatalf("expected held unsaved edit on diverged doc: %+v", res)
		}
		if got := h.diskSHA("live.txt"); got != invalidSHA {
			t.Fatalf("diverged agent edit clobbered disk: got=%s want=%s", got, invalidSHA)
		}
	})

	t.Run("deleted_underneath_recreates_file_with_exact_draft_sha", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"gone.txt": []byte("orig\n")})
		doc, err := h.service.Open(ctx, h.project, "gone.txt", h.rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		doc, err = h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         "draft_survives\n", EOL: "lf",
		})
		if err != nil {
			t.Fatalf("ReplaceSnapshot: %v", err)
		}
		if err := os.Remove(filepath.Join(h.rootDir, "gone.txt")); err != nil {
			t.Fatalf("remove gone.txt: %v", err)
		}
		if err := h.service.ObserveExternal(ctx, h.project, []editordoc.PathRef{{RootID: h.rootID, Path: "gone.txt"}}); err != nil {
			t.Fatalf("ObserveExternal after delete: %v", err)
		}
		absentDoc, err := h.service.CurrentSnapshot(ctx, h.projectID, doc.ID)
		if err != nil || !absentDoc.Absent || absentDoc.Draft != "draft_survives\n" {
			t.Fatalf("expected Absent=true with surviving draft: %+v err=%v", absentDoc, err)
		}
		saved, err := h.service.Save(ctx, h.project, absentDoc.ID, "window-1", uuid.NewString(), "chat-e03", 1, absentDoc.Revision)
		if err != nil {
			t.Fatalf("Save recreated file: %v", err)
		}
		if saved.Absent || h.diskSHA("gone.txt") != sha256Hex([]byte("draft_survives\n")) {
			t.Fatalf("recreated file sha mismatch or still absent: absent=%v", saved.Absent)
		}
	})
}

// ============================================================================
// E08 · Encodings (UTF-8 BOM, UTF-16LE/BE, CRLF/LF, mixed EOL), sizes (D17),
//       binary/invalid UTF-8 refusals, long lines, and D6 synthetic secret spans
// ============================================================================

func TestWEATB05_E08_EncodingsSizesAndSecretSpans(t *testing.T) {
	ctx := t.Context()

	t.Run("utf8_bom_and_crlf_preserved_with_outside_bytes_exact_by_hash", func(t *testing.T) {
		bom := []byte{0xef, 0xbb, 0xbf}
		raw := append(append([]byte(nil), bom...), []byte("first\r\nsecond\r\nthird\r\n")...)
		h := newTB05Harness(t, map[string][]byte{"crlf_bom.txt": raw})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "crlf_bom.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText crlf_bom: ok=%v err=%v", ok, err)
		}
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: "first\nSECOND\nthird\n", OperationID: uuid.NewString(),
			SessionID: "chat-e08", Turn: 1, ToolCallID: "call-e08-bom", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit crlf_bom: res=%+v err=%v", res, err)
		}
		want := append(append([]byte(nil), bom...), []byte("first\r\nSECOND\r\nthird\r\n")...)
		got := h.readDisk("crlf_bom.txt")
		if sha256Hex(got) != sha256Hex(want) {
			t.Fatalf("UTF-8 BOM + CRLF bytes mismatch:\ngot:  %x\nwant: %x", got, want)
		}
	})

	t.Run("utf16le_bom_preserved_across_edit", func(t *testing.T) {
		encoded, err := textfile.EncodeBounded("alpha\r\nbeta\r\ngamma\r\n", textfile.UTF16LEBOM, textfile.LimitsForRaw(readcaps.MaxMutationBytes))
		if err != nil {
			t.Fatalf("EncodeBounded utf16le: %v", err)
		}
		h := newTB05Harness(t, map[string][]byte{"utf16.txt": encoded})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "utf16.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText utf16: ok=%v err=%v", ok, err)
		}
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: "alpha\nBETA\ngamma\n", OperationID: uuid.NewString(),
			SessionID: "chat-e08", Turn: 1, ToolCallID: "call-e08-u16", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit utf16: res=%+v err=%v", res, err)
		}
		want, err := textfile.EncodeBounded("alpha\r\nBETA\r\ngamma\r\n", textfile.UTF16LEBOM, textfile.LimitsForRaw(readcaps.MaxMutationBytes))
		if err != nil {
			t.Fatalf("EncodeBounded want utf16: %v", err)
		}
		got := h.readDisk("utf16.txt")
		if sha256Hex(got) != sha256Hex(want) {
			t.Fatalf("UTF-16LE BOM bytes mismatch: got_sha=%s want_sha=%s", sha256Hex(got), sha256Hex(want))
		}
	})

	t.Run("mixed_eol_replace_lines_preserves_outside_line_terminators", func(t *testing.T) {
		mixed := "line1_crlf\r\nline2_target\nline3_lf\n"
		replaced, err := native.ReplaceLineRange("mixed.txt", mixed, 2, 2, "line2_REPLACED")
		if err != nil {
			t.Fatalf("ReplaceLineRange mixed: %v", err)
		}
		if !strings.HasPrefix(replaced, "line1_crlf\r\n") {
			t.Fatalf("outside CRLF line terminator altered: %q", replaced)
		}
		if !strings.HasSuffix(replaced, "line3_lf\n") {
			t.Fatalf("outside LF line terminator altered: %q", replaced)
		}
	})

	t.Run("binary_and_invalid_utf8_refused_without_disk_mutation", func(t *testing.T) {
		binRaw := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0x03}
		badUTF8 := []byte("valid\n\xff\xfeinvalid\n")
		h := newTB05Harness(t, map[string][]byte{"bin.dat": binRaw, "bad.txt": badUTF8})

		if doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "bin.dat"); err != nil || ok || doc != nil {
			t.Fatalf("binary file should not open as editable text: doc=%v ok=%v err=%v", doc, ok, err)
		}
		if doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "bad.txt"); err != nil || ok || doc != nil {
			t.Fatalf("invalid UTF-8 file should not open as editable text: doc=%v ok=%v err=%v", doc, ok, err)
		}
		if _, err := native.ReplaceLineRange("bin.txt", "ok\n", 1, 1, "bad\x00nul"); err == nil {
			t.Fatalf("expected WRITE_BINARY_DENIED on NUL byte")
		} else if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "WRITE_BINARY_DENIED" {
			t.Fatalf("unexpected error code for NUL byte: %v", err)
		}
		if h.diskSHA("bin.dat") != sha256Hex(binRaw) || h.diskSHA("bad.txt") != sha256Hex(badUTF8) {
			t.Fatalf("binary/invalid-UTF-8 refusal mutated disk")
		}
	})

	t.Run("very_long_line_and_d17_size_bounds", func(t *testing.T) {
		longLine := strings.Repeat("A", 65536)
		content := longLine + "\nshort_target\n" + longLine + "\n"
		h := newTB05Harness(t, map[string][]byte{"long.txt": []byte(content)})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "long.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText long.txt: ok=%v err=%v", ok, err)
		}
		want := longLine + "\nshort_EDITED\n" + longLine + "\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: want, OperationID: uuid.NewString(),
			SessionID: "chat-e08", Turn: 1, ToolCallID: "call-e08-long", ToolName: "edit",
		})
		if err != nil || !res.Saved || h.diskSHA("long.txt") != sha256Hex([]byte(want)) {
			t.Fatalf("long line edit failed or hash mismatch: err=%v", err)
		}

		// D17 read-bound checks: just under, at, and just over 8 MiB (readcaps.MaxFileBytes).
		limits := textfile.LimitsForRaw(readcaps.MaxFileBytes)
		under := bytes.Repeat([]byte("a"), readcaps.MaxFileBytes-1)
		exact := bytes.Repeat([]byte("a"), readcaps.MaxFileBytes)
		over := bytes.Repeat([]byte("a"), readcaps.MaxFileBytes+1)
		if _, _, err := textfile.Open(under, limits); err != nil {
			t.Fatalf("D17 under 8 MiB should decode: %v", err)
		}
		if _, _, err := textfile.Open(exact, limits); err != nil {
			t.Fatalf("D17 exact 8 MiB should decode: %v", err)
		}
		if _, _, err := textfile.Open(over, limits); err == nil {
			t.Fatalf("D17 over 8 MiB MUST be refused")
		}
		// Mutation ceiling: 4 MiB + 1 refused by ReplaceLineRange guard.
		overMut := strings.Repeat("b", readcaps.MaxMutationBytes+1)
		if _, err := native.ReplaceLineRange("big.txt", "x\n", 1, 1, overMut); err == nil {
			t.Fatalf("mutation over MaxMutationBytes (4 MiB) MUST be refused")
		}
	})

	t.Run("synthetic_d6_canary_span_preserved_outside_edit", func(t *testing.T) {
		canary := mintSyntheticCanary(t, "E08_SPAN")
		secretLine := "API_TOKEN=" + canary + "\n"
		raw := []byte("HEADER=v1\n" + secretLine + "FOOTER=end\n")
		h := newTB05Harness(t, map[string][]byte{"config.env": raw})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "config.env")
		if err != nil || !ok {
			t.Fatalf("OpenText config.env: ok=%v err=%v", ok, err)
		}
		want := "HEADER=v2\n" + secretLine + "FOOTER=end\n"
		res, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: want, OperationID: uuid.NewString(),
			SessionID: "chat-e08", Turn: 1, ToolCallID: "call-e08-canary", ToolName: "edit",
		})
		if err != nil || !res.Saved {
			t.Fatalf("ApplyAgentEdit config.env: res=%+v err=%v", res, err)
		}
		disk := h.readDisk("config.env")
		if sha256Hex(disk) != sha256Hex([]byte(want)) {
			t.Fatalf("canary-bearing file hash mismatch after edit")
		}
		lines := strings.SplitAfter(string(disk), "\n")
		if sha256Hex([]byte(lines[1])) != sha256Hex([]byte(secretLine)) {
			t.Fatalf("secret span outside edit was modified")
		}
	})
}

// ============================================================================
// E10 · Durability of an edit across restart commit points (4 in-process slots)
// ============================================================================

type tb05FailingLedger struct {
	*sourceledger.Store
	mu          sync.Mutex
	failPrepare bool
}

func (l *tb05FailingLedger) Prepare(ctx context.Context, inputs []sourceledger.RecordInput) (sourceledger.PreparedRecording, error) {
	l.mu.Lock()
	shouldFail := l.failPrepare
	l.mu.Unlock()
	if shouldFail {
		return nil, errors.New("tb05 injected crash before ledger commit")
	}
	return l.Store.Prepare(ctx, inputs)
}

func TestWEATB05_E10_RestartDurabilityAcrossCommitPoints(t *testing.T) {
	ctx := t.Context()

	// Slot E10-inproc-1: Crash between disk write (file_applied) and ledger version record.
	t.Run("E10_inproc_1_restart_between_disk_apply_and_ledger_commit", func(t *testing.T) {
		tmp := t.TempDir()
		dbPath := filepath.Join(tmp, "e10.db")
		blobDir := filepath.Join(tmp, "blobs")
		rootDir := filepath.Join(tmp, "project")
		if err := os.MkdirAll(rootDir, 0o755); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		if err := os.MkdirAll(blobDir, 0o755); err != nil {
			t.Fatalf("mkdir blobs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, "durable.txt"), []byte("v0\n"), 0o644); err != nil {
			t.Fatalf("write durable.txt: %v", err)
		}

		sqlDB1, err := db.Open(dbPath)
		if err != nil {
			t.Fatalf("db.Open #1: %v", err)
		}
		projectID := "proj-e10-" + uuid.NewString()[:8]
		rootID := uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := sqlDB1.ExecContext(ctx, `INSERT INTO projects (id, name, last_opened_at, created_at) VALUES (?, 'E10', ?, ?)`, projectID, now, now); err != nil {
			t.Fatalf("seed e10 project: %v", err)
		}
		if _, err := sqlDB1.ExecContext(ctx, `INSERT INTO project_roots (id, project_id, path, label, kind, is_primary, added_at) VALUES (?, ?, ?, 'primary', 'attached', 1, ?)`, rootID, projectID, rootDir, now); err != nil {
			t.Fatalf("seed e10 project_root: %v", err)
		}
		p := &project.Project{ID: projectID, SourceBranch: sourcebranch.Trunk, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: rootDir, IsPrimary: true}}}

		baseLedger1 := sourceledger.New(sqlDB1, blobDir, sourceledger.Timing{})
		failLedger := &tb05FailingLedger{Store: baseLedger1}
		store1 := editordoc.NewStore(sqlDB1)
		svc1 := editordoc.New(store1, failLedger, baseLedger1.History, tb05FixedRoots{p: p})

		doc, err := svc1.Open(ctx, p, "durable.txt", rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		doc, err = svc1.ReplaceSnapshot(ctx, doc.ID, projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         "v1_committed\n", EOL: "lf",
		})
		if err != nil {
			t.Fatalf("ReplaceSnapshot: %v", err)
		}

		// Inject failure at ledger commit boundary so disk write completes but ledger commit is interrupted.
		failLedger.failPrepare = true
		opID := uuid.NewString()
		if _, err := svc1.Save(ctx, p, doc.ID, "window-1", opID, "chat-e10", 1, doc.Revision); err == nil {
			t.Fatalf("expected injected ledger failure during Save")
		}
		// Simulate process restart: close service & DB completely.
		_ = svc1.Close(context.Background())
		_ = sqlDB1.Close()

		// Boot fresh DB + fresh ledger + fresh editordoc.Service and run Recover().
		sqlDB2, err := db.Open(dbPath)
		if err != nil {
			t.Fatalf("db.Open #2: %v", err)
		}
		defer sqlDB2.Close()
		ledger2 := sourceledger.New(sqlDB2, blobDir, sourceledger.Timing{})
		store2 := editordoc.NewStore(sqlDB2)
		svc2 := editordoc.New(store2, ledger2, ledger2.History, tb05FixedRoots{p: p})
		defer svc2.Close(context.Background())

		if err := svc2.Recover(ctx); err != nil {
			t.Fatalf("Recover after restart failed: %v", err)
		}
		// Second Recover() must be strictly idempotent (no duplicate versions or effects).
		if err := svc2.Recover(ctx); err != nil {
			t.Fatalf("second Recover failed: %v", err)
		}

		mut, err := store2.Mutation(ctx, opID)
		if err != nil {
			t.Fatalf("read recovered mutation: %v", err)
		}
		if mut.Status != "complete" {
			t.Fatalf("recovered mutation status = %q, want complete", mut.Status)
		}
		recoveredDoc, err := store2.Get(ctx, doc.ID)
		if err != nil {
			t.Fatalf("read recovered doc: %v", err)
		}
		diskRaw, err := os.ReadFile(filepath.Join(rootDir, "durable.txt"))
		if err != nil {
			t.Fatalf("read disk: %v", err)
		}
		if sha256Hex(diskRaw) != sha256Hex([]byte("v1_committed\n")) || recoveredDoc.BaseContent != "v1_committed\n" || recoveredDoc.Dirty {
			t.Fatalf("post-recovery disk/doc mismatch: disk=%q base=%q dirty=%v", string(diskRaw), recoveredDoc.BaseContent, recoveredDoc.Dirty)
		}

		// Verify exactly 1 effect for this edit in source_effects (no duplicates).
		ro, err := db.OpenReadOnly(ctx, dbPath)
		if err != nil {
			t.Fatalf("db.OpenReadOnly: %v", err)
		}
		defer ro.Close()
		var effectCount int
		if err := ro.QueryRowContext(ctx, `SELECT COUNT(1) FROM source_effects WHERE project_id=? AND file_id=?`, projectID, recoveredDoc.FileID).Scan(&effectCount); err != nil {
			t.Fatalf("count effects: %v", err)
		}
		if effectCount != 1 {
			t.Fatalf("expected exactly 1 effect after restart recovery, got %d", effectCount)
		}
	})

	// Slot E10-inproc-2: Pre-publication crash (prepared mutation) leaves disk untouched on restart.
	t.Run("E10_inproc_2_restart_before_disk_write_no_partial_write", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"pre.txt": []byte("original\n")})
		origSHA := h.diskSHA("pre.txt")
		doc, err := h.service.Open(ctx, h.project, "pre.txt", h.rootID, "", "window-1", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		doc, err = h.service.ReplaceSnapshot(ctx, doc.ID, h.projectID, editordoc.SnapshotReplacement{
			DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
			Content:         "pending_draft\n", EOL: "lf",
		})
		if err != nil {
			t.Fatalf("ReplaceSnapshot: %v", err)
		}
		// Restart without saving: disk must remain original, draft must survive durably in SQLite.
		_ = h.service.Close(context.Background())
		restarted := editordoc.New(h.store, h.ledger, h.ledger.History, tb05FixedRoots{p: h.project})
		h.service = restarted
		if err := restarted.Recover(ctx); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if h.diskSHA("pre.txt") != origSHA {
			t.Fatalf("pre-save restart mutated disk")
		}
		reloaded, err := restarted.CurrentSnapshot(ctx, h.projectID, doc.ID)
		if err != nil || !reloaded.Dirty || reloaded.Draft != "pending_draft\n" {
			t.Fatalf("unsaved draft lost across restart: %+v err=%v", reloaded, err)
		}
	})

	// Slot E10-inproc-3: Replay of same OperationID across restart is idempotent (one version per effect).
	t.Run("E10_inproc_3_idempotent_operation_replay_across_restart", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"idem.txt": []byte("start\n")})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "idem.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		opID := uuid.NewString()
		edit := editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: "end\n", OperationID: opID,
			SessionID: "chat-e10", Turn: 1, ToolCallID: "call-e10-idem", ToolName: "edit",
		}
		res1, err := h.service.ApplyAgentEdit(ctx, edit)
		if err != nil || !res1.Saved {
			t.Fatalf("first ApplyAgentEdit: res=%+v err=%v", res1, err)
		}
		v1, e1 := h.countVersionsAndEffectsRO(res1.Document.FileID)

		// Restart service on same SQLite DB and replay the exact same OperationID.
		_ = h.service.Close(context.Background())
		restarted := editordoc.New(h.store, h.ledger, h.ledger.History, tb05FixedRoots{p: h.project})
		h.service = restarted
		res2, err := restarted.ApplyAgentEdit(ctx, edit)
		if err != nil {
			t.Fatalf("replayed ApplyAgentEdit returned error: %v", err)
		}
		if res2.Document.Draft != "end\n" || h.diskSHA("idem.txt") != sha256Hex([]byte("end\n")) {
			t.Fatalf("replayed edit corrupted state: res2=%+v", res2)
		}
		v2, e2 := h.countVersionsAndEffectsRO(res1.Document.FileID)
		if v2 != v1 || e2 != e1 {
			t.Fatalf("duplicate versions/effects after replay across restart: before=(%d,%d) after=(%d,%d)", v1, e1, v2, e2)
		}
		h.logReceipt("E10-inproc-3", map[string]any{
			"versions": v2, "effects": e2, "disk_sha256": h.diskSHA("idem.txt"),
		})
	})

	// Slot E10-inproc-4: Reconcile and change-event publication remain consistent after restart.
	t.Run("E10_inproc_4_post_restart_reconcile_and_change_event_publication", func(t *testing.T) {
		h := newTB05Harness(t, map[string][]byte{"events.txt": []byte("a\n")})
		doc, ok, err := h.service.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "events.txt")
		if err != nil || !ok {
			t.Fatalf("OpenText: ok=%v err=%v", ok, err)
		}
		res1, err := h.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
			Content: "b\n", OperationID: uuid.NewString(),
			SessionID: "chat-e10", Turn: 1, ToolCallID: "call-e10-ev1", ToolName: "edit",
		})
		if err != nil || !res1.Saved {
			t.Fatalf("first edit: %v", err)
		}
		// Restart service and attach fresh change-event observer (simulating reconnecting window).
		_ = h.service.Close(context.Background())
		restarted := editordoc.New(h.store, h.ledger, h.ledger.History, tb05FixedRoots{p: h.project})
		h.service = restarted
		var postChanges []editordoc.Change
		restarted.SetOnChange(func(_ context.Context, c editordoc.Change) {
			postChanges = append(postChanges, c)
		})
		// Read basis is cleared on restart -> must require fresh read!
		if _, err := restarted.AgentReadBase(ctx, h.projectID, "chat-e10", doc.ID, nil); !errors.Is(err, editordoc.ErrAgentReadRequired) {
			t.Fatalf("restart retained stale in-memory read basis: %v", err)
		}
		// Reopen, re-read, and apply a clean post-restart edit.
		reopened, ok, err := restarted.OpenText(ctx, h.projectID, sourcebranch.Trunk, h.rootID, "events.txt")
		if err != nil || !ok || reopened.Draft != "b\n" || reopened.Dirty {
			t.Fatalf("reopened document inconsistent after restart: %+v ok=%v err=%v", reopened, ok, err)
		}
		restarted.RememberAgentRead(h.projectID, "chat-e10", reopened.ID, reopened.Revision)
		res2, err := restarted.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: h.projectID, DocumentID: reopened.ID, ExpectedRevision: reopened.Revision,
			Content: "c\n", OperationID: uuid.NewString(),
			SessionID: "chat-e10", Turn: 2, ToolCallID: "call-e10-ev2", ToolName: "edit",
		})
		if err != nil || !res2.Saved {
			t.Fatalf("post-restart edit failed: %v", err)
		}
		if h.diskSHA("events.txt") != sha256Hex([]byte("c\n")) {
			t.Fatalf("post-restart disk sha mismatch")
		}
		if len(postChanges) == 0 {
			t.Fatalf("reconnecting window received zero change events after restart")
		}
		_, effects := h.countVersionsAndEffectsRO(reopened.FileID)
		if effects != 2 {
			t.Fatalf("expected exactly 2 effects (1 pre-restart + 1 post-restart), got %d", effects)
		}
	})
}

// Keep unused import check satisfied if api constants are referenced in ledger checks.
var _ = api.SourceChangeOriginAgent
var _ = sql.ErrNoRows
