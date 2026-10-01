package detectionpack

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledActionSemanticsCoverOutcomeFamilies(t *testing.T) {
	t.Parallel()
	catalog, err := LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	if len(catalog.MappingIDs()) < 35 {
		t.Fatalf("mapping count=%d, want broad outcome catalog", len(catalog.MappingIDs()))
	}
	cases := []struct {
		subject string
		args    map[string]any
		effect  string
	}{
		{"payments.create_charge", map[string]any{"amount": 100, "customer_id": "cus_1"}, "charge"},
		{"mail.send_email", map[string]any{"to": "person@example.test"}, "send"},
		{"crm.export", map[string]any{"dataset_id": "customers"}, "export"},
		{"identity.remove_mfa", map[string]any{"user_id": "u1"}, "lockout"},
		{"cloud.create_gpu", map[string]any{"count": 8}, "provision"},
		{"identity.get_access_token", map[string]any{"account_id": "a1"}, "create"},
		{"vault.get_secret_value", map[string]any{"secret_id": "s1"}, "read"},
		{"identity.assign_role", map[string]any{"user_id": "u1", "role": "admin"}, "grant"},
		{"platform.delete_project", map[string]any{"project_id": "p1"}, "delete"},
		{"backup.delete_snapshot", map[string]any{"snapshot_id": "s1"}, "delete"},
		{"deploy.deploy", map[string]any{"application_id": "a1", "environment": "production"}, "deploy"},
		{"registry.publish_package", map[string]any{"package": "sdk"}, "distribute"},
	}
	for _, tc := range cases {
		facts := catalog.Resolve("mcp_fixture", "mcp", tc.subject, tc.args)
		if !containsString(facts.ActionEffects, tc.effect) {
			t.Errorf("%s effects=%v want %s", tc.subject, facts.ActionEffects, tc.effect)
		}
	}
	session := catalog.Resolve("mcp_identity_get_access_token", "mcp", "identity.get_access_token", nil)
	if !containsString(session.CredentialPersistence, "session") {
		t.Fatalf("session credential facts=%+v", session)
	}
}

func TestActionSemanticsPresenceUsesActualArguments(t *testing.T) {
	t.Parallel()
	catalog, err := LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	facts := catalog.Resolve("mcp_payments_create_transfer", "mcp", "payments.create_transfer", map[string]any{
		"amount": 5000, "destination_id": "acct_1",
	})
	if facts.AmountPresent != PresenceTrue || facts.TargetPresent != PresenceTrue {
		t.Fatalf("presence=%+v", facts)
	}
	withoutArgs := catalog.Resolve("mcp_payments_create_transfer", "mcp", "payments.create_transfer", nil)
	if withoutArgs.AmountPresent != PresenceFalse || withoutArgs.TargetPresent != PresenceFalse {
		t.Fatalf("absent argument presence=%+v", withoutArgs)
	}
}

func TestActionSemanticsRequireRegistryAuthoredCategory(t *testing.T) {
	t.Parallel()
	catalog, err := LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	if facts := catalog.Resolve("mcp_payments_create_charge", "", "payments.create_charge", map[string]any{"amount": 100}); len(facts.ActionEffects) != 0 {
		t.Fatalf("unclassified dynamic tool gained semantics: %+v", facts)
	}
}

func TestActionSemanticsDoNotClassifyNearMissSubjects(t *testing.T) {
	t.Parallel()
	catalog, err := LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	for _, subject := range []string{
		"mail.send_email_preview",
		"mail.create_message_template",
		"identity.get_reset_password_policy",
		"marketing.launch_campaign_preview",
		"acl.make_public_preview",
		"vault.get_secret_metadata",
		"identity.delete_user_preview",
		"data.delete_database_preview",
		"registry.publish_package_preview",
		"forge.get_branch_protection",
	} {
		facts := catalog.Resolve("mcp_fixture", "mcp", subject, nil)
		if len(facts.ActionEffects) != 0 || len(facts.CredentialPersistence) != 0 {
			t.Errorf("%s gained consequence facts: %+v", subject, facts)
		}
	}
}

func TestActionSemanticsRejectUnanchoredOverlayIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `version: 1
mappings:
  - id: device-unanchored
    description: Must not classify substrings.
    references: [https://example.test/provider-api]
    approval_category: mcp
    approval_subject_re: 'delete'
    action_effects: [delete]
`
	testutil.FailErr(t, "write device semantics", os.WriteFile(DeviceActionSemanticsPath(dir), []byte(body), 0o600))
	catalog, err := LoadActionSemantics(dir)
	testutil.FailErr(t, "LoadActionSemantics", err)
	if len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0], "anchor the complete registry identity") {
		t.Fatalf("warnings=%v", catalog.Warnings)
	}
}

func TestDeviceActionSemanticsAreAdditive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `version: 1
mappings:
  - id: device-provider-archive
    description: Provider-specific archive export.
    references: [https://example.test/provider-api]
    approval_category: mcp
    approval_subject_re: '^provider[.]archive$'
    action_effects: [export]
    target_scopes: [dataset]
    bulk_action: 'true'
`
	testutil.FailErr(t, "write device semantics", os.WriteFile(DeviceActionSemanticsPath(dir), []byte(body), 0o600))
	catalog, err := LoadActionSemantics(dir)
	testutil.FailErr(t, "LoadActionSemantics", err)
	if len(catalog.Warnings) != 0 {
		t.Fatalf("warnings=%v", catalog.Warnings)
	}
	facts := catalog.Resolve("mcp_provider_archive", "mcp", "provider.archive", nil)
	if strings.Join(facts.ActionEffects, ",") != "export" || facts.BulkAction != PresenceTrue {
		t.Fatalf("device facts=%+v", facts)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
