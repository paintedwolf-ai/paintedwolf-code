package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryRejectsAmbiguousOrIncompleteRoutes(t *testing.T) {
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	source := []byte("SELECT 1")
	sum := sha256.Sum256(source)
	step := Step{Source: source, ID: "history-label", Checksum: hex.EncodeToString(sum[:]), From: first, To: second, Apply: func(context.Context, *sql.Tx) error { return nil }}
	for name, steps := range map[string][]Step{
		"mutated definition": {{ID: step.ID, Checksum: step.Checksum, Source: []byte("SELECT 2"), From: first, To: second, Apply: step.Apply}},
		"missing definition": {{ID: step.ID, Checksum: step.Checksum, From: first, To: second, Apply: step.Apply}},
		"missing step":       nil,
		"duplicate source":   {step, step},
		"reverse step":       {{ID: step.ID, Checksum: step.Checksum, Source: source, From: second, To: first, Apply: step.Apply}},
		"invalid checksum":   {{ID: step.ID, Checksum: "changed", Source: source, From: first, To: second, Apply: step.Apply}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(second, []Baseline{first}, steps); err == nil {
				t.Fatal("invalid registry accepted")
			}
		})
	}
	registry, err := New(second, []Baseline{first}, []Step{step})
	testutil.FailErr(t, "register valid route", err)
	for _, unsupported := range []Baseline{
		{Revision: 1, Shape: second.Shape},
		{Revision: 3, Shape: first.Shape},
		{Revision: 0, Shape: first.Shape},
	} {
		if _, err := registry.Plan(unsupported); err == nil {
			t.Fatalf("unsupported schema accepted: %v", unsupported)
		}
	}
	if _, err := New(second, []Baseline{{Revision: 2, Shape: first.Shape}}, nil); err == nil {
		t.Fatal("released baseline redefinition accepted")
	}
}

func TestPlanBudgetsEverySkippedMigrationWithoutOverflow(t *testing.T) {
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	third := Baseline{Revision: 3, Shape: strings.Repeat("c", 64)}
	apply := func(context.Context, *sql.Tx) error { return nil }
	source := []byte("SELECT 1")
	sum := sha256.Sum256(source)
	steps := []Step{
		{ID: "one", Checksum: hex.EncodeToString(sum[:]), Source: source, From: first, To: second, Apply: apply, ScratchBytes: 128},
		{ID: "two", Checksum: hex.EncodeToString(sum[:]), Source: source, From: second, To: third, Apply: apply, ScratchBytes: 256},
	}
	registry, err := New(third, []Baseline{first, second}, steps)
	testutil.FailErr(t, "register budgeted route", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan skipped route", err)
	if plan.ScratchBytes() != 384 {
		t.Fatalf("scratch=%d want384", plan.ScratchBytes())
	}
	plan, err = registry.Plan(third)
	testutil.FailErr(t, "plan current store", err)
	if plan.ScratchBytes() != 0 {
		t.Fatal("current store has migration scratch budget")
	}
	steps[1].ScratchBytes = ^uint64(0)
	if _, err := New(third, []Baseline{first, second}, steps); err == nil {
		t.Fatal("overflowed working-space budget accepted")
	}
}
