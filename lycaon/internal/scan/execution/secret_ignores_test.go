package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectignore"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecretLedgerReclassifiesWithoutLosingRawFindings(t *testing.T) {
	ctx := t.Context()
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "fixture-scanner", "execution")
	_, err := scanignore.AddIgnoreEntry(root, scanignore.IgnoreEntry{Rule: "fixture-token", Reason: "old scanner decision", Expires: "2000-01-01"})
	testutil.FailErr(t, "write lapsed scanner decision", err)
	finding := scanfindings.FixtureFinding("fixture-token", api.FindingLevelHigh, "fixture", "a.go", 3)
	finding.Tool.DriverID = "fixture-scanner"
	finding.Properties.Lycaon.Kind = api.FindingKindSecret
	first := ledgerScan(t, store, uuid.NewString(), root, "fixture-scanner", api.ScanTargetFull, api.ScanCoverageComplete, finding)
	first.ExecutionFingerprint = "execution"
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(ctx, &first, []api.SecurityFinding{finding}, nil, time.Now().UTC()))
	result := &scanoutput.Result{Findings: []api.SecurityFinding{finding}, SecretIdentities: []scanoutput.SecretIdentity{{FindingIndex: 0, ValueFingerprint: "fixture-one"}}}
	testutil.FailErr(t, "record identity", store.SaveSecretIdentities(ctx, first.ID, result))
	active := true
	store.SecretIgnores = func(context.Context, string) map[string]projectignore.SecretEntry {
		if !active {
			return nil
		}
		return map[string]projectignore.SecretEntry{"fixture-one": {ID: "public-example", Value: "fixture", Reason: "public fixture"}}
	}
	_, err = store.SyncIgnores(ctx, root, []string{root})
	testutil.FailErr(t, "apply fixture decision", err)
	if state := ledgerStates(t, store, root)[finding.RuleID]; state != api.FindingLedgerIgnored {
		t.Fatalf("accepted value state=%s", state)
	}
	active = false
	_, err = store.SyncIgnores(ctx, root, []string{root})
	testutil.FailErr(t, "revoke fixture decision", err)
	if state := ledgerStates(t, store, root)[finding.RuleID]; state == api.FindingLedgerIgnored {
		t.Fatal("withdrawal required rescan")
	}
	active = true
	second := ledgerScan(t, store, uuid.NewString(), root, "fixture-scanner", api.ScanTargetFull, api.ScanCoverageComplete, finding)
	result.SecretIdentities[0].ValueFingerprint = "different-value"
	testutil.FailErr(t, "record changed value", store.SaveSecretIdentities(ctx, second.ID, result))
	testutil.FailErr(t, "invalidate", store.InvalidateIgnoreDigest(ctx, root))
	_, err = store.SyncIgnores(ctx, root, []string{root})
	testutil.FailErr(t, "reclassify changed value", err)
	if state := ledgerStates(t, store, root)[finding.RuleID]; state == api.FindingLedgerIgnored {
		t.Fatal("new bytes inherited old location's exception")
	}
}
