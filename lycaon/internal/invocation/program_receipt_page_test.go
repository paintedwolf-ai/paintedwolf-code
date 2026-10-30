package invocation_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// attachStream retains screened bytes for one child stream through the actual
// output lifetime, so the page reads the stored preview, not test data.
func attachStream(t *testing.T, f programReceiptFixture, lifetime *invocation.ProgramOutputRetention, store blobstore.Store, quota *outputQuota, execution, stream, reference, body string, previewBytes int) {
	t.Helper()
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString(body))), tooloutput.StreamSpillOptions{Store: store, ClaimID: execution + ":" + reference, MaximumBytes: 1 << 20, PreviewBytes: previewBytes, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain screened "+stream, err)
	testutil.FailErr(t, "attach "+stream, lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{ExecutionID: execution, Stream: stream, Reference: reference, Complete: true}, spill))
}

func conflictedWrite(t *testing.T, f programReceiptFixture) (string, string) {
	t.Helper()
	write := f.childFor(t, "write")
	background := f.child(t)
	refused := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "TEXT_WRITE_CONFLICT", EffectCertainty: toolcontract.NoEffect}
	conflict := &toolcontract.RevisionConflict{Path: "src/report.txt", ExpectedRevision: "rev:3", ObservedRevision: "rev:5"}
	testutil.FailErr(t, "settle stale destination", f.recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: refused, DetailReference: "output:conflict", AcceptedRevision: "editor:4", Conflict: conflict}))
	completed := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}
	f.start(t, background)
	testutil.FailErr(t, "settle background starter", f.recorder.SettleChild(t.Context(), background.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: completed, OwnerInvoked: true, DetailReference: "output:job", JobHandle: "job:watch-7"}))
	return write.Invocation().Identity.ExecutionID, background.Invocation().Identity.ExecutionID
}

func TestProgramPageCarriesChildOutputsJobsAndConflicts(t *testing.T) {
	f := newProgramReceiptFixture(t)
	writeID, backgroundID := conflictedWrite(t, f)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "admit output lifetime", err)
	testutil.FailErr(t, "restore empty output set", lifetime.Restore(t.Context()))
	attachStream(t, f, lifetime, store, quota, backgroundID, "stdout", "output:first-stdout", "first run", 16)
	attachStream(t, f, lifetime, store, quota, backgroundID, "stdout", "output:latest-stdout", "x"+strings.Repeat("é", 3000), 16<<10)
	attachStream(t, f, lifetime, store, quota, backgroundID, "stderr", "output:stderr", "warning: slow", 64)
	attachStream(t, f, lifetime, store, quota, backgroundID, "detail", "output:detail-body", "detail", 16)

	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 20)
	testutil.FailErr(t, "read page", err)
	byID := map[string]wire.CommandExecutionReceipt{}
	for _, row := range page.Executions {
		byID[row.ID] = row
	}
	write, background := byID[writeID], byID[backgroundID]
	if c := write.RevisionConflict; c == nil || c.Path != "src/report.txt" || c.ExpectedRevision != "rev:3" || c.ObservedRevision != "rev:5" || write.AcceptedRevision != "editor:4" || write.PublishedRevision != "" || write.JobHandle != "" || len(write.Outputs) != 0 {
		t.Fatalf("stale destination facts = %+v", write)
	}
	if background.JobHandle != "job:watch-7" || background.RevisionConflict != nil || len(background.Outputs) != 2 {
		t.Fatalf("background child = %+v", background)
	}
	stderr, stdout := background.Outputs[0], background.Outputs[1]
	if stdout.Stream != "stdout" || stdout.Ref != "output:latest-stdout" || stdout.SizeBytes != 6001 || !stdout.Complete || !stdout.PreviewTruncated || len(stdout.Preview) != 2047 || !strings.HasPrefix(stdout.Preview, "xé") || strings.ContainsRune(stdout.Preview, '�') {
		t.Fatalf("stdout preview = %+v (%d bytes)", stdout, len(stdout.Preview))
	}
	if stderr.Stream != "stderr" || stderr.Preview != "warning: slow" || stderr.PreviewTruncated {
		t.Fatalf("stderr preview = %+v", stderr)
	}
}

func TestChildSettlementRefusesMalformedJobsAndConflicts(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.childFor(t, "write")
	refused := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "TEXT_WRITE_CONFLICT", EffectCertainty: toolcontract.NoEffect}
	for name, settlement := range map[string]toolcontract.ProgramChildSettlement{
		"same revisions":   {Conflict: &toolcontract.RevisionConflict{Path: "a", ExpectedRevision: "r1", ObservedRevision: "r1"}},
		"missing path":     {Conflict: &toolcontract.RevisionConflict{ExpectedRevision: "r1", ObservedRevision: "r2"}},
		"oversized":        {Conflict: &toolcontract.RevisionConflict{Path: strings.Repeat("p", 5000), ExpectedRevision: "r1", ObservedRevision: "r2"}},
		"job prose":        {JobHandle: "started a job"},
		"oversized handle": {JobHandle: strings.Repeat("j", 300)},
	} {
		settlement.Result, settlement.DetailReference = refused, "output:refused"
		if err := f.recorder.SettleChild(t.Context(), child.Invocation().Identity.ExecutionID, settlement); err == nil {
			t.Errorf("%s settled", name)
		}
	}
}

func TestProgramReceiptRouteServesChildFactsFromTheStore(t *testing.T) {
	var database db.Handle
	srv := contractfixture.NewTestServerWithWorkflows(t, func(d *api.Dependencies) {
		database = d.Core.Database
		d.Core.Invocations = invocation.NewSQLRecorder(d.Core.Database)
	})
	const session = "11111111-2222-4333-8444-555555555555"
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	f := programReceiptFixtureOn(t, database, session, map[string]any{"command": "${op} arg"})
	writeID, backgroundID := conflictedWrite(t, f)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+session+"/program-receipts/"+f.program, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Executions []map[string]any `json:"executions"`
	}
	testutil.FailErr(t, "decode raw page", json.Unmarshal(rec.Body.Bytes(), &raw))
	facts := map[string]map[string]any{}
	for _, row := range raw.Executions {
		facts[row["id"].(string)] = row
	}
	conflict, _ := facts[writeID]["revision_conflict"].(map[string]any)
	if conflict["path"] != "src/report.txt" || conflict["expected_revision"] != "rev:3" || conflict["observed_revision"] != "rev:5" {
		t.Fatalf("wire conflict = %v", facts[writeID])
	}
	if facts[backgroundID]["job_handle"] != "job:watch-7" || facts[backgroundID]["revision_conflict"] != nil || facts[writeID]["outputs"] != nil {
		t.Fatalf("wire child facts = %v / %v", facts[backgroundID], facts[writeID])
	}
}
