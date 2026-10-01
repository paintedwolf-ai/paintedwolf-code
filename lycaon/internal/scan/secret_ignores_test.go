package scan

import (
	"testing"

	"github.com/lycaon/lycaon/internal/projectignore"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecretExceptionRequiresEveryValueBehindFinding(t *testing.T) {
	finding := scanfindings.FixtureFinding("fixture", api.FindingLevelHigh, "fixture", "fixture.env", 1)
	finding.Properties.Lycaon.Kind = api.FindingKindSecret
	result := &scanoutput.Result{Findings: []api.SecurityFinding{finding, finding}, SecretIdentities: []scanoutput.SecretIdentity{{FindingIndex: 0, ValueFingerprint: "accepted"}}}
	decisions := map[string]projectignore.SecretEntry{"accepted": {Value: "fixture", Reason: "example"}}
	active, ignored := partitionSecretIgnores(result, result.Findings, decisions)
	if len(active) != 2 || len(ignored) != 0 {
		t.Fatal("unknown value inherited colliding finding identity")
	}
	result.SecretIdentities = append(result.SecretIdentities, scanoutput.SecretIdentity{FindingIndex: 1, ValueFingerprint: "different"})
	active, ignored = partitionSecretIgnores(result, result.Findings, decisions)
	if len(active) != 2 || len(ignored) != 0 {
		t.Fatal("unaccepted value inherited colliding finding identity")
	}
	decisions["different"] = projectignore.SecretEntry{Value: "another fixture", Reason: "example"}
	active, ignored = partitionSecretIgnores(result, result.Findings, decisions)
	if len(active) != 0 || len(ignored) != 2 || len(result.Findings) != 2 {
		t.Fatal("classification lost raw findings or required a duplicate finding rule")
	}
}
