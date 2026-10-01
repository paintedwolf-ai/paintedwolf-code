package scan

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssessmentMemberAdmissionPreservesGroupBoundary(t *testing.T) {
	store := authorityTestStore(t)
	coordinator := &CoordinatorImpl{Store: store}
	group := AssessmentDraft{
		ID: "group", CanonicalPath: t.TempDir(), SourceSnapshotID: "source",
		RequiredScanners: []string{"sast", "sca"}, Trigger: api.ScanTriggerWriteBurst,
		Target: TargetSelection{Kind: api.ScanTargetPaths, Paths: []string{"older.go", "newer.go"}, DeletedPaths: []string{"obsolete.go"}},
	}
	_, err := store.EnsureAssessment(t.Context(), group)
	testutil.FailErr(t, "record group assessment", err)
	for _, tc := range []struct {
		name   string
		change func(*AssessmentDraft, *EnqueueRequest)
		valid  bool
	}{
		{"member subset with independent trigger", func(*AssessmentDraft, *EnqueueRequest) {}, true},
		{"different source", func(m *AssessmentDraft, _ *EnqueueRequest) { m.SourceSnapshotID = "other" }, false},
		{"different root", func(m *AssessmentDraft, _ *EnqueueRequest) { m.CanonicalPath += "/other" }, false},
		{"outside target", func(m *AssessmentDraft, _ *EnqueueRequest) { m.Target.Paths = []string{"outside.go"} }, false},
		{"outside deletion", func(m *AssessmentDraft, _ *EnqueueRequest) { m.Target.DeletedPaths = []string{"outside.go"} }, false},
		{"unrequired scanner", func(_ *AssessmentDraft, r *EnqueueRequest) { r.ScannerID = "secrets" }, false},
		{"changed required scanners", func(m *AssessmentDraft, _ *EnqueueRequest) { m.RequiredScanners = []string{"sast"} }, false},
		{"changed target kind", func(m *AssessmentDraft, _ *EnqueueRequest) { m.Target.Kind = api.ScanTargetFull }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			member := group
			member.Target.Paths = []string{"newer.go"}
			member.Target.DeletedPaths = nil
			member.Trigger = api.ScanTriggerAuthorityRefresh
			req := EnqueueRequest{ScannerID: "sast", Assessment: &group}
			tc.change(&member, &req)
			err := coordinator.ensureEnqueueAssessment(t.Context(), req, member)
			if tc.valid {
				testutil.FailErr(t, "admit group member", err)
			} else if !errors.Is(err, ErrAssessmentIdentityMismatch) {
				t.Fatalf("member admission error=%v, want assessment identity refusal", err)
			}
			_, err = store.EnsureAssessment(t.Context(), group)
			testutil.FailErr(t, "group identity remains intact", err)
		})
	}
}
