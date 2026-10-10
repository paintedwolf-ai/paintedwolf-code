package projectsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Prepare with the production planner, interrupt at its effect boundary, then
// execute only the selected filesystem phase before constructing a fresh service.
func TestRecoveryClassifiesExecutedEffectsAcrossOperationKinds(t *testing.T) {
	for _, kind := range []string{"create", "copy", "rename", "delete", "restore"} {
		for _, boundary := range []string{"prepared", "published", "file_applied"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				service, p, root, _ := sourceMutationFixture(t)
				testutil.FailErr(t, "seed recovery source", os.WriteFile(filepath.Join(root, "source"), []byte("retained"), 0600))
				prior := ""
				if kind == "restore" {
					prior = uuid.NewString()
					testutil.FailErr(t, "prepare restore history", service.Delete(t.Context(), prior, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "source"}))
				}
				id := uuid.NewString()
				interruption := errors.New("fixture crash at effect boundary")
				ctx := WithSourceEffect(t.Context(), func() error { panic(interruption) })
				func() {
					defer func() {
						if got := recover(); got != interruption {
							t.Fatalf("operation did not reach crash boundary: %v", got)
						}
					}()
					startRecoveryOperation(t, ctx, service, p, kind, id, prior)
				}()
				row, found, err := service.Journal.load(t.Context(), id)
				testutil.FailErr(t, "load crash intent", err)
				if !found || row.Status != sourceMutationPrepared {
					t.Fatalf("missing prepared intent: %+v", row)
				}
				if kind == "copy" || kind == "create" {
					if row.Plan.NativeTrash == nil || nativeTrashReceiptRecorded(&row.Plan) {
						t.Fatal("fixture lacks future Undo reservation")
					}
				}
				if boundary != "prepared" {
					testutil.FailErr(t, "execute filesystem effect without settlement", service.Effects.applyMutation(t.Context(), row))
					if boundary == "file_applied" {
						row.Status = sourceMutationFileApplied
					}
					testutil.FailErr(t, "persist selected interruption point", service.Journal.update(t.Context(), row))
				}
				restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
				installTestTrash(t, restarted)
				testutil.FailErr(t, "recover executed effects", restarted.Recover(t.Context()))
				recovered, found, err := restarted.Journal.load(t.Context(), id)
				testutil.FailErr(t, "load recovery classification", err)
				want := sourceMutationCommitted
				if boundary == "prepared" {
					want = sourceMutationFailed
				}
				if !found || recovered.Status != want {
					t.Fatalf("classification=%+v want=%s", recovered, want)
				}
				if boundary != "prepared" {
					history, err := restarted.History.State(t.Context(), p.ID)
					testutil.FailErr(t, "read settled history", err)
					if kind == "restore" {
						if history.Redo == nil {
							t.Fatal("completed restore did not settle Redo")
						}
					} else if history.Undo == nil {
						t.Fatal("completed effect did not settle Undo")
					}
				}
				testutil.FailErr(t, "recover again without replay", restarted.Recover(t.Context()))
				assertRecoveryEffectFiles(t, root, kind, boundary)
			})
		}
	}
}

func startRecoveryOperation(t *testing.T, ctx context.Context, s *SourceMutationService, p *Project, kind, id, prior string) {
	t.Helper()
	root := p.Roots[0].ID
	switch kind {
	case "create":
		_, _ = s.Create(ctx, id, p, SourceEntryCreateRequest{RootID: root, Path: "output", Kind: SourceEntryFile})
	case "copy":
		_, _ = s.Copy(ctx, id, p, SourceCopyRequest{RootID: root, From: "source", To: "output"})
	case "rename":
		_, _ = s.Rename(ctx, id, p, SourceRenameRequest{RootID: root, From: "source", To: "output"})
	case "delete":
		_ = s.Delete(ctx, id, p, SourceDeleteRequest{RootID: root, Path: "source"})
	case "restore":
		_, _ = s.Undo(ctx, id, p, SourceHistoryMutationRequest{ExpectedEntryID: prior})
	default:
		t.Fatalf("unknown recovery operation %s", kind)
	}
}

func assertRecoveryEffectFiles(t *testing.T, root, kind, boundary string) {
	t.Helper()
	applied := boundary != "prepared"
	sourcePresent := kind != "restore"
	outputPresent := false
	if applied {
		sourcePresent = kind != "delete" && kind != "rename"
		outputPresent = kind == "create" || kind == "copy" || kind == "rename"
	}
	for name, want := range map[string]bool{"source": sourcePresent, "output": outputPresent} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if !want {
			if !os.IsNotExist(err) {
				t.Fatalf("unexpected recovered %s: %v", name, err)
			}
			continue
		}
		testutil.FailErr(t, "read recovered "+name, err)
		expected := "retained"
		if kind == "create" && name == "output" {
			expected = ""
		}
		if string(data) != expected {
			t.Fatalf("recovered %s=%q want=%q", name, data, expected)
		}
	}
}

func TestRecoveryRefusesUnacknowledgedNativeMoves(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(map[bool]string{false: "before native move", true: "after native move before receipt"}[moved], func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			testutil.FailErr(t, "seed native move", os.WriteFile(filepath.Join(root, "source"), []byte("retained"), 0600))
			trash := filepath.Join(t.TempDir(), "source")
			crash := errors.New("fixture crash before native acknowledgement")
			service.Effects.SetTrashMover(func(context.Context, string) (desktoptrash.Receipt, error) {
				if moved {
					_, err := testTrashRelocate(filepath.Join(root, "source"), trash)
					testutil.FailErr(t, "execute native move", err)
				}
				panic(crash)
			})
			id := uuid.NewString()
			func() {
				defer func() {
					if got := recover(); got != crash {
						t.Fatalf("native crash boundary=%v", got)
					}
				}()
				_ = service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "source"})
			}()
			restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
			testutil.FailErr(t, "classify unacknowledged move", restarted.Recover(t.Context()))
			row, found, err := restarted.Journal.load(t.Context(), id)
			testutil.FailErr(t, "read native recovery classification", err)
			if !found || row.Status != sourceMutationFailed || nativeTrashReceiptRecorded(&row.Plan) {
				t.Fatalf("invented executed native recovery: %+v", row)
			}
			history, err := restarted.History.State(t.Context(), p.ID)
			testutil.FailErr(t, "read unacknowledged history", err)
			if history.Undo != nil {
				t.Fatalf("unacknowledged move invented Undo: %+v", history)
			}
			preserved := filepath.Join(root, "source")
			if moved {
				preserved = trash
			}
			data, err := os.ReadFile(preserved)
			testutil.FailErr(t, "read preserved native item", err)
			if string(data) != "retained" {
				t.Fatalf("native recovery changed item: %q", data)
			}
			if moved {
				err = restarted.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "source"})
				if !errors.Is(err, ErrSourceTrashUnavailable) {
					t.Fatalf("explicit replay invented receipt: %v", err)
				}
			}
		})
	}
}
