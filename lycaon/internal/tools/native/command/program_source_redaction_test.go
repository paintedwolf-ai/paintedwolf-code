package command

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

type noopProgramProvenance struct{}

func (noopProgramProvenance) BeginProgramPublication(context.Context, string, string) error {
	return nil
}

func (noopProgramProvenance) StageProgramPublication(context.Context, evidence.ProgramProvenanceTarget, evidence.ProgramHTTPProvenance) error {
	return nil
}

func (noopProgramProvenance) CommitProgramPublication(context.Context, string, string) error {
	return nil
}

func (noopProgramProvenance) MarkProgramPublicationMaterialized(context.Context, string, string) error {
	return nil
}

func (noopProgramProvenance) ListRecoverableProgramPublications(context.Context, string, int) ([]evidence.ProgramPublicationKey, error) {
	return nil, nil
}

func (noopProgramProvenance) ExternalCustodyURLsForPaths(context.Context, string, string, []string) ([]string, error) {
	return nil, nil
}

func TestBeginProgramRedactedSourceClearsSourceExact(t *testing.T) {
	db := testdbfixture.Open(t, "source-redaction.db")
	testdbseed.InsertSession(t, db, "sess-redacted", testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(db)
	rawToken := "PW_SYNTH_SECRET_ABCDEF123456"
	matcher := &secretmatch.Matcher{}
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher.SetFingerprinter(fp)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name:           "token",
			Secret:         rawToken,
			RuleID:         secretmatch.ManagedRuleID,
			Title:          secretmatch.ManagedRuleTitle,
			Source:         secretmatch.SourceRememberedMatch,
			NonDisclosable: true,
		}}
	})
	blobs := blobstore.Store{Root: t.TempDir()}
	runtime := &ProgramRuntime{
		Recorder:   recorder,
		Blobs:      blobs,
		Projector:  captureprojection.New(matcher, nil),
		Provenance: noopProgramProvenance{},
	}

	source := "echo " + rawToken
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20})
	testutil.FailErr(t, "program memory", err)
	parsed, lease, err := parseProgram(t.Context(), memory, "command", source)
	testutil.FailErr(t, "parse program", err)
	defer func() { _ = parsed.Close() }()
	defer lease.Close()

	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	parentReceipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID:  testdbseed.DefaultProjectID,
		SessionID:  "sess-redacted",
		ToolCallID: "call-redacted",
		ToolName:   "command",
		Args:       map[string]any{"command": source},
		Contract:   contract,
	})
	testutil.FailErr(t, "begin parent invocation", err)

	run := &programRun{
		source:        source,
		memory:        memory,
		parentReceipt: parentReceipt.ID,
		tc:            tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-redacted", ToolCallID: "call-redacted"}},
	}
	run.root.Identity = toolcontract.ExecutionIdentity{
		ProgramID:   "prog-redacted",
		ExecutionID: "exec-root",
	}

	outputs, err := invocation.NewProgramOutputRetention(t.Context(), recorder, blobs, "sess-redacted", runtime.sessionResources("sess-redacted"))
	testutil.FailErr(t, "new program output retention", err)
	testutil.FailErr(t, "restore output retention", outputs.Restore(t.Context()))
	defer func() { _ = outputs.Close(t.Context()) }()

	screening := captureprojection.StreamOptions{
		WorkingMemory: memory,
		Bounds: secretmatch.StreamBounds{
			InputBytes:   invocation.MaximumProgramInputBytes,
			OutputBytes:  invocation.MaximumProgramInputBytes,
			MemoryBytes:  32 << 20,
			DetectorWork: 1 << 30,
		},
	}
	err = runtime.beginProgram(t.Context(), run, outputs, lease.Plan, captureprojection.Scope{SessionID: "sess-redacted"}, screening)
	testutil.FailErr(t, "beginProgram with secret-bearing command source", err)

	var sourceExact int
	err = db.QueryRowContext(t.Context(), `SELECT source_exact FROM invocation_programs WHERE id = ?`, "prog-redacted").Scan(&sourceExact)
	testutil.FailErr(t, "query source_exact", err)
	if sourceExact != 0 {
		t.Fatalf("expected source_exact=0 for redacted command source, got %d", sourceExact)
	}

	var redacted int
	var preview string
	err = db.QueryRowContext(t.Context(), `SELECT redacted, preview FROM invocation_program_outputs WHERE program_id = ? AND stream = 'source'`, "prog-redacted").Scan(&redacted, &preview)
	testutil.FailErr(t, "query source output", err)
	if redacted != 1 {
		t.Fatalf("expected redacted=1 for command source output, got %d", redacted)
	}
	if strings.Contains(preview, rawToken) {
		t.Fatalf("raw secret leaked into durable source preview: %q", preview)
	}
}
