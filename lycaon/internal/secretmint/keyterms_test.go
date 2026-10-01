package secretmint

import (
	"reflect"
	"testing"
)

func TestSplitKeySegments(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want []string
	}{
		{"WEBHOOK_SECRET", []string{"webhook", "secret"}},
		{"stripeApiKey", []string{"stripe", "api", "key"}},
		{"stripeAPIKey", []string{"stripe", "api", "key"}},
		{"signing-token", []string{"signing", "token"}},
		{"password", []string{"password"}},
		{"oauth2ClientSecret", []string{"oauth2", "client", "secret"}},
		{"", nil},
	} {
		if got := splitKeySegments(tc.key); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitKeySegments(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// Unlisted names can match trailing credential terms.
func TestCredentialSlotReadsUnlistedNames(t *testing.T) {
	ins := testInspector(t)
	for _, key := range []string{
		"WEBHOOK_SECRET",
		"stripeApiKey",
		"signing-token",
		"DEPLOY_TOKEN",
		"clientCredentials",
		"SESSION_PASSPHRASE",
		"ENCRYPTION_KEY",
		"password",
	} {
		if _, ok := ins.credentialSlot(key, ins.envKeys); !ok {
			t.Errorf("credentialSlot(%q) = false, want true", key)
		}
	}
}

// Trailing qualifiers distinguish policy, path, count, and identifier fields.
func TestCredentialSlotIgnoresQualifiedNames(t *testing.T) {
	ins := testInspector(t)
	for _, key := range []string{
		"password_policy",
		"PASSWORD_FILE",
		"password_min_length",
		"TOKEN_URL",
		"api_key_id",
		"SECRET_NAME",
		"rotation_token_ttl",
	} {
		if _, ok := ins.credentialSlot(key, ins.envKeys); ok {
			t.Errorf("credentialSlot(%q) = true, want false — the head noun is not a credential", key)
		}
	}
}

// Single key and auth terms require a matching pair.
func TestCredentialSlotNeedsThePairForAmbiguousHeads(t *testing.T) {
	ins := testInspector(t)
	for _, key := range []string{"SORT_KEY", "primaryKey", "CACHE_KEY", "AUTH"} {
		if _, ok := ins.credentialSlot(key, ins.envKeys); ok {
			t.Errorf("credentialSlot(%q) = true, want false", key)
		}
	}
	for _, key := range []string{"API_KEY", "privateKey", "SIGNING_KEY", "auth_token"} {
		if _, ok := ins.credentialSlot(key, ins.envKeys); !ok {
			t.Errorf("credentialSlot(%q) = false, want true", key)
		}
	}
}

// An enumerated vendor name reports the catalog's spelling.
func TestCredentialSlotKeepsEnumeratedVendorNames(t *testing.T) {
	ins := testInspector(t)
	canon, ok := ins.credentialSlot("rabbitmq_default_pass", ins.envKeys)
	if !ok {
		t.Fatal("enumerated vendor key must still match")
	}
	if canon != "RABBITMQ_DEFAULT_PASS" {
		t.Errorf("canon = %q, want the catalog spelling", canon)
	}
}
