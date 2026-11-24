package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type tracedFileAdmission struct {
	err error
}

func (*tracedFileAdmission) Start(string) {}

func (f *tracedFileAdmission) Await(ctx context.Context, _ tools.CredentialWait) error {
	if trace := secretmatch.EvidenceTraceFrom(ctx); trace != nil {
		trace.NoteRootGenerationWait(5, "root-1", "build-42", 1, true, 3, 18, 2)
	}
	return f.err
}

func (*tracedFileAdmission) Delivered(context.Context, tools.CredentialFileRead) error { return nil }
func (*tracedFileAdmission) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}
func (*tracedFileAdmission) ForgetProject(context.Context, string) error { return nil }

func TestModelScreeningValuesEmitsValueFreeEvidencePhases(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	database := testdbfixture.Open(t, "evidence_diag.db")
	testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x41}, 32))
	testutil.FailErr(t, "fingerprinter", err)

	admission := &tracedFileAdmission{}
	runtime := New(t.Context(), database, nil, nil, nil)
	runtime.CredentialFiles = admission
	runtime.Fingerprinter, runtime.Harvest = fp, secretharvest.NewRuntime(fp)
	registry, authority := tools.NewDefaultRegistry(), &capabilityAuthority{}
	testutil.FailErr(t, "bootstrap capabilities", runtime.BuildCapabilities(registry, authority))
	_, err = authority.service.Generate(t.Context(), secretcap.GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
		SessionID: "root-1", OperationID: "gen-diag", Name: "diag token", Purpose: "screening",
	})
	testutil.FailErr(t, "generate secret", err)
	generated := runtime.Harvest.ValuesFor("root-1")
	if len(generated) == 0 || generated[0].Secret() == "" {
		t.Fatal("expected generated secret value")
	}
	secretValue := generated[0].Secret()

	var mu sync.Mutex
	var records []secretmatch.ScreenPhaseRecord
	unsub := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
		mu.Lock()
		records = append(records, rec)
		mu.Unlock()
	})
	defer unsub()

	ctx := secretmatch.WithScreenCallID(t.Context(), "call-evidence-1")
	values, err := runtime.ModelScreeningValues(ctx, testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "model screening values", err)
	if len(values) == 0 {
		t.Fatal("expected managed screening values")
	}

	admission.err = errors.New("index unavailable")
	if _, err := runtime.ModelScreeningValues(secretmatch.WithScreenCallID(t.Context(), "call-evidence-fail"), testdbseed.DefaultProjectID, "root-1"); err == nil {
		t.Fatal("expected EnsureProject error")
	}

	// Cancel context to exercise managed_value_lookup failure branch.
	admission.err = nil
	canceledCtx, cancel := context.WithCancel(secretmatch.WithScreenCallID(t.Context(), "call-lookup-fail"))
	cancel()
	_, _ = runtime.ModelScreeningValues(canceledCtx, testdbseed.DefaultProjectID, "root-1")

	mu.Lock()
	defer mu.Unlock()
	var sawLookup, sawReconciled, sawFail bool
	for _, rec := range records {
		raw, mErr := json.Marshal(rec)
		testutil.FailErr(t, "marshal record", mErr)
		if strings.Contains(string(raw), secretValue) {
			t.Fatalf("phase record leaked managed secret value: %s", string(raw))
		}
		switch {
		case rec.CallID == "call-evidence-1" && rec.Phase == "managed_value_lookup":
			sawLookup = rec.ManagedValues == len(values) && !rec.Failed
		case rec.CallID == "call-evidence-1" && rec.Phase == "evidence_reconciled":
			sawReconciled = rec.RootsChecked == 1 && rec.BuildsJoined == 1 && rec.PathsClassified == 18 && rec.CredentialFilesRead == 2 && !rec.Failed
		case rec.CallID == "call-evidence-fail" && rec.Phase == "evidence_reconciled":
			sawFail = rec.Failed
		}
	}
	if !sawLookup || !sawReconciled || !sawFail {
		t.Fatalf("missing expected phase records: lookup=%v reconciled=%v fail=%v records=%+v", sawLookup, sawReconciled, sawFail, records)
	}
}
