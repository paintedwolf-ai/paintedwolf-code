package approvals

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretGrantWireProjectionHidesFingerprints(t *testing.T) {
	const fingerprint = "sf1_host_only_fingerprint"
	expires := time.Now().UTC().Add(time.Hour)
	rows := ApprovalGrants([]hitl.ApprovalGrant{{
		ID: "grant-secret", Scope: hitl.ApprovalGrantScopeProject,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategorySecret), Pattern: "set-digest"},
		Title:     "Allow for 1 hour", GrantedAt: time.Now().UTC(), ExpiresAt: &expires,
		SecretFingerprints: []string{fingerprint},
	}})
	if len(rows) != 1 || rows[0].Pattern != "1 detected secret" {
		t.Fatalf("wire row = %+v", rows)
	}
	raw, err := json.Marshal(rows)
	testutil.FailErr(t, "encode wire rows", err)
	if strings.Contains(string(raw), fingerprint) || strings.Contains(string(raw), "set-digest") {
		t.Fatalf("wire projection exposed secret authority identity: %s", raw)
	}
}
