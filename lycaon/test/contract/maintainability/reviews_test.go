package maintainability

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func reviewFixture(t *testing.T) (*workingTree, sizebudget.Policy, *inventory, artifactReview) {
	t.Helper()
	boundary := []byte("stable operation contract")
	tree := &workingTree{bodies: map[string][]byte{"pkg/contract.go": boundary}}
	policy := sizebudget.Policy{Limits: map[string]sizebudget.Limit{"source_files": {Warn: 10, Limit: 30}}}
	inv := &inventory{measured: newMeasurements()}
	inv.measured["source_files"]["pkg/service.go"] = 15
	review := artifactReview{Category: "source_files", Artifact: "pkg/service.go", ArtifactReview: sizebudget.ArtifactReview{
		Kind: "runtime_service", Workstream: "host-lifecycle", Disposition: "retain", Reason: "One ordered shutdown boundary.",
		ReviewedCommit: strings.Repeat("a", 40), Ceiling: 20,
		Boundaries: map[string]string{"pkg/contract.go": fmt.Sprintf("%x", sha256.Sum256(boundary))},
	}}
	return tree, policy, inv, review
}

func putReview(t *testing.T, tree *workingTree, name string, review artifactReview) {
	t.Helper()
	body, err := json.Marshal(review)
	testutil.FailErr(t, "encode review", err)
	// State is derived in the measured snapshot.
	var authored map[string]any
	testutil.FailErr(t, "decode review fixture", json.Unmarshal(body, &authored))
	delete(authored, "state")
	body, err = json.Marshal(authored)
	testutil.FailErr(t, "encode authored review", err)
	tree.paths = append(tree.paths, name)
	tree.bodies[name] = body
}

func TestReviewAcceptanceRenewsOnGrowthAndBoundaryChanges(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		mutate      func(*workingTree, *inventory)
	}{
		{"retained", "retained", func(*workingTree, *inventory) {}},
		{"at ceiling", "retained", func(_ *workingTree, inv *inventory) { inv.measured["source_files"]["pkg/service.go"] = 20 }},
		{"growth", "ceiling_exceeded", func(_ *workingTree, inv *inventory) { inv.measured["source_files"]["pkg/service.go"] = 21 }},
		{"boundary", "boundary_changed", func(tree *workingTree, _ *inventory) { tree.bodies["pkg/contract.go"] = []byte("changed") }},
		{"missing boundary", "boundary_changed", func(tree *workingTree, _ *inventory) { delete(tree.bodies, "pkg/contract.go") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, policy, inv, review := reviewFixture(t)
			putReview(t, tree, reviewDirectory+"/service.json", review)
			tc.mutate(tree, inv)
			report := trackingReport(policy, inv, func(string, string) bool { return false })
			testutil.FailErr(t, "attach reviews", attachReviews(tree, policy, inv, report))
			if got := report.Artifacts[0].Review; got == nil || got.State != tc.state {
				t.Fatalf("review state: %+v, want %s", got, tc.state)
			}
			if report.Artifacts[0].EffectiveCap != 30 {
				t.Fatal("review changed admission cap")
			}
		})
	}
}

func TestReviewsRejectInvalidPolicyAndStaleIdentities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*artifactReview)
	}{
		{"category", func(r *artifactReview) { r.Category = "unknown" }},
		{"renamed artifact", func(r *artifactReview) { r.Artifact = "pkg/new.go" }},
		{"kind", func(r *artifactReview) { r.Kind = "owner" }},
		{"workstream", func(r *artifactReview) { r.Workstream = "unsafe\nvalue" }},
		{"disposition", func(r *artifactReview) { r.Disposition = "ignore" }},
		{"reason", func(r *artifactReview) { r.Reason = " " }},
		{"commit", func(r *artifactReview) { r.ReviewedCommit = "main" }},
		{"ceiling", func(r *artifactReview) { r.Ceiling = 31 }},
		{"warning", func(r *artifactReview) { r.Ceiling = 10 }},
		{"boundaries", func(r *artifactReview) { r.Boundaries = nil }},
		{"boundary path", func(r *artifactReview) { r.Boundaries = map[string]string{"../escape": strings.Repeat("a", 64)} }},
		{"boundary digest", func(r *artifactReview) { r.Boundaries = map[string]string{"pkg/contract.go": "bad"} }},
		{"authored state", func(r *artifactReview) { r.State = "retained" }},
		{"refactor acceptance", func(r *artifactReview) { r.Disposition = "refactor" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, policy, inv, review := reviewFixture(t)
			tc.mutate(&review)
			if err := validateReview(review, policy, inv); err == nil {
				t.Fatal("invalid review accepted")
			}
		})
	}
}

func TestReviewFilesRejectUnknownFieldsTrailingContentAndDuplicates(t *testing.T) {
	for _, mode := range []string{"unknown", "trailing", "duplicate", "extension", "duplicate key", "state"} {
		t.Run(mode, func(t *testing.T) {
			tree, policy, inv, review := reviewFixture(t)
			name := reviewDirectory + "/service.json"
			putReview(t, tree, name, review)
			switch mode {
			case "duplicate key":
				tree.bodies[name] = []byte(`{"category":"source_files","category":"test_files"}`)
			case "state":
				tree.bodies[name] = []byte(`{"state":""}`)
			case "unknown":
				tree.bodies[name] = []byte(`{"unexpected":true}`)
			case "trailing":
				tree.bodies[name] = append(tree.bodies[name], []byte(" {}")...)
			case "duplicate":
				putReview(t, tree, reviewDirectory+"/duplicate.json", review)
			case "extension":
				tree.paths = append(tree.paths, reviewDirectory+"/bad.txt")
			}
			if err := attachReviews(tree, policy, inv, trackingReport(policy, inv, func(string, string) bool { return false })); err == nil {
				t.Fatal("invalid review files accepted")
			}
		})
	}
}

func TestRefactorReviewAndBelowWarningReviewRemainExplicit(t *testing.T) {
	tree, policy, inv, review := reviewFixture(t)
	review.Disposition, review.Ceiling, review.Boundaries = "refactor", 0, nil
	putReview(t, tree, reviewDirectory+"/service.json", review)
	report := trackingReport(policy, inv, func(string, string) bool { return false })
	testutil.FailErr(t, "attach refactor review", attachReviews(tree, policy, inv, report))
	if report.Artifacts[0].Review.State != "refactor" {
		t.Fatal("refactor decision lost")
	}
	inv.measured["source_files"]["pkg/service.go"] = 9
	report = trackingReport(policy, inv, func(string, string) bool { return false })
	testutil.FailErr(t, "retain below-warning review", attachReviews(tree, policy, inv, report))
	if len(report.Artifacts) != 0 {
		t.Fatal("review created artificial warning")
	}
}
