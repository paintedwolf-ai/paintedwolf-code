package settings

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

const retainedApprovalSettings = `approval_posture: balanced
rules:
  - category: host
    pattern: blocked.example
    effect: deny
grants:
  - id: retained-host
    category: host
    pattern: allowed.example
    title: Allow host
    scope: device
`

func incompatibleSecretLease(expires time.Time, unknownField bool) string {
	lease := fmt.Sprintf(`  - id: incompatible-secret
    category: secret
    scope: project
    project_id: project-id
    pattern: %s
    title: Allow for 1 hour
    expires_at: %s
    secret_destination_id: provider
    secret_fingerprints: [sf1_one]
`, secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"sf1_one"}), expires.Format(time.RFC3339))
	if unknownField {
		lease += "    secret_surface: model_request\n"
	}
	return lease
}

func TestIncompatibleSecretLeaseRequiresExplicitRecovery(t *testing.T) {
	for _, unknownField := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("unknown-field=%t/expired=%t", unknownField, expired), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "approvals.yaml")
				ttl := time.Hour
				if expired {
					ttl = -time.Hour
				}
				raw := []byte(retainedApprovalSettings + incompatibleSecretLease(time.Now().UTC().Add(ttl), unknownField))
				testutil.FailErr(t, "write incompatible approvals", os.WriteFile(path, raw, 0o600))
				store, err := NewApprovalStoreAt(path)
				if err == nil || store != nil {
					t.Fatalf("incompatible authority loaded: store=%v, err=%v", store, err)
				}
				for _, detail := range []string{path, "file is unchanged", "remove only that grant"} {
					if !strings.Contains(err.Error(), detail) {
						t.Fatalf("recovery error missing %q: %v", detail, err)
					}
				}
				if !unknownField && !strings.Contains(err.Error(), "incompatible-secret") {
					t.Fatalf("invalid grant is unidentified: %v", err)
				}
				after, readErr := os.ReadFile(path)
				testutil.FailErr(t, "read refused approvals", readErr)
				if !bytes.Equal(after, raw) {
					t.Fatal("refusing incompatible authority changed the approvals file")
				}
			})
		}
	}
}

func TestSecretLeaseRecoveryPreservesOtherApprovalsAndRequiresReapproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.yaml")
	lease := incompatibleSecretLease(time.Now().UTC().Add(time.Hour), true)
	raw := []byte(retainedApprovalSettings + lease)
	testutil.FailErr(t, "write incompatible approvals", os.WriteFile(path, raw, 0o600))
	if _, err := NewApprovalStoreAt(path); err == nil {
		t.Fatal("incompatible lease was accepted")
	}
	recovered := bytes.TrimSuffix(raw, []byte(lease))
	testutil.FailErr(t, "remove reviewed incompatible lease", os.WriteFile(path, recovered, 0o600))
	store, err := NewApprovalStoreAt(path)
	testutil.FailErr(t, "load recovered approvals", err)
	if store.Posture() != gate.PostureBalanced || len(store.OverlayRules()) != 1 {
		t.Fatal("recovery lost posture or policy")
	}
	grants := store.GlobalGrants()
	if len(grants) != 1 || grants[0].ID != "retained-host" {
		t.Fatalf("recovery changed unrelated grants: %+v", grants)
	}
	approvalGate := NewRuleApprovalGate(store, NoSources()).(*RuleApprovalGate)
	if releaseCovered(approvalGate, "", "project-id", "provider", "model_request", []string{"sf1_one"}) {
		t.Fatal("removing the incompatible lease retained secret authority")
	}
	putSecretGrant(t, store, "reviewed-secret", "project-id", t.TempDir(), []secretmatch.SecretFingerprint{"sf1_one"})
	reloaded, err := NewApprovalStoreAt(path)
	testutil.FailErr(t, "reload reviewed recipient lease", err)
	approvalGate = NewRuleApprovalGate(reloaded, NoSources()).(*RuleApprovalGate)
	if !releaseCovered(approvalGate, "", "project-id", "provider", "model_request", []string{"sf1_one"}) {
		t.Fatal("new approval did not restore the reviewed recipient authority")
	}
	if releaseCovered(approvalGate, "", "project-id", "other-provider", "model_request", []string{"sf1_one"}) {
		t.Fatal("new approval widened to another recipient")
	}
}
