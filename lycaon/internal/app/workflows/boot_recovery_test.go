package workflows

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Repair accounting must replay against verdicts settled by startup recovery.
func TestBuildOrdersReviewRepairsAfterVerdictRecovery(t *testing.T) {
	database := testdbfixture.Open(t, "workflow-recovery.db")
	entries := make(map[string]bootrecovery.Entry)
	_, err := Build(t.Context(), Dependencies{
		Database: database, DataDir: t.TempDir(), Sessions: store.NewSQL(database),
		AuthzRecorder: authzcontext.SQLRecorder(database),
	}, func(entry bootrecovery.Entry) error {
		if _, duplicate := entries[entry.Name]; duplicate {
			t.Fatalf("duplicate recovery owner %q", entry.Name)
		}
		entries[entry.Name] = entry
		return nil
	})
	testutil.FailErr(t, "build workflow recovery graph", err)
	repairs, present := entries["workflow-review-repairs"]
	if !present || repairs.Kind != bootrecovery.KindJournal || repairs.Phase != bootrecovery.PhaseServe || repairs.Run == nil {
		t.Fatalf("review repair journal is absent or malformed: %+v", repairs)
	}
	if !slices.Contains(repairs.After, "workflow-verdicts") {
		t.Fatalf("review repair journal dependencies = %v, want settled verdicts", repairs.After)
	}
	for _, verdictFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled verdicts", true: "unsettled verdicts"}[verdictFails], func(t *testing.T) {
			registry := bootrecovery.New()
			var executed []string
			verdictFailure := errors.New("verdict journal remained unsettled")
			for _, entry := range entries {
				entry.Run = func(context.Context) error {
					executed = append(executed, entry.Name)
					if entry.Name == "workflow-verdicts" && verdictFails {
						return verdictFailure
					}
					return nil
				}
				testutil.FailErr(t, "register actual recovery topology", registry.Register(entry))
			}
			report, err := registry.Run(t.Context(), bootrecovery.PhaseServe)
			testutil.FailErr(t, "run workflow recovery topology", err)
			verdictIndex := slices.Index(executed, "workflow-verdicts")
			repairIndex := slices.Index(executed, "workflow-review-repairs")
			if verdictIndex < 0 || (!verdictFails && repairIndex <= verdictIndex) || (verdictFails && repairIndex >= 0) {
				t.Fatalf("recovery execution = %v, verdict failure = %v", executed, verdictFails)
			}
			if verdictFails {
				for _, outcome := range report.Outcomes {
					if outcome.Name == "workflow-review-repairs" && slices.Contains(outcome.Blocked, "workflow-verdicts") {
						return
					}
				}
				t.Fatalf("unsettled verdicts did not block repair accounting: %+v", report.Outcomes)
			}
		})
	}
}
