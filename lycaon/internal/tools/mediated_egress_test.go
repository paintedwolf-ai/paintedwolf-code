package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
)

type mediatedRecorder struct {
	records []authzledger.MediatedEndpointRecord
}

func (*mediatedRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}
func (*mediatedRecorder) AppendCapabilityRecord(context.Context, authzledger.CapabilityRecord) error {
	return nil
}
func (*mediatedRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}
func (r *mediatedRecorder) AppendMediatedEndpoint(_ context.Context, rec authzledger.MediatedEndpointRecord) {
	r.records = append(r.records, rec)
}

func TestRecordMediatedEgressKeepsOriginatingTool(t *testing.T) {
	recorder := &mediatedRecorder{}
	RecordMediatedEgress(context.Background(), ToolContext{
		SessionID: "session", AuthzRecorder: recorder,
	}, "verify", []confine.EgressHost{{Host: "example.test", Port: 443, Transport: "tcp", Allowed: true, Attempts: 2}})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want one", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Tool != "verify" || len(record.Endpoints) != 1 || record.Endpoints[0].Host != "example.test" {
		t.Fatalf("mediated record = %+v", record)
	}
}
