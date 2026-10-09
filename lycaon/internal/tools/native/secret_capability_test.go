package native_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestSecretCapabilityToolsExposeReferencesAndLifecycleOnly(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "root-chat", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	},
		func(id string) bool { _, err := uuid.Parse(id); return err == nil },
	)
	service := secretcap.NewWithStore(database, values, nil)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register secret tools", native.RegisterSecretCapabilityTools(reg, service))
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID,
			SessionID:  "root-chat",
			ToolCallID: "generate-call"},
	}

	generated := invokeSecretTool(t, reg, native.SecretGenerateTool, map[string]any{
		"name": "Webhook signing key",
	}, tctx)
	var created struct {
		Secret struct {
			Reference string `json:"reference"`
			State     string `json:"state"`
		} `json:"secret"`
		ValueDisclosed bool `json:"value_disclosed"`
	}
	testutil.FailErr(t, "decode generated secret", json.Unmarshal([]byte(generated), &created))
	id, err := secretcap.ParseReference(created.Secret.Reference)
	testutil.FailErr(t, "parse generated reference", err)
	// Stored values are keyed by version.
	version, err := db.New(database).GetCurrentManagedSecretVersion(t.Context(), id)
	testutil.FailErr(t, "read current version", err)
	value, ok := values.Get(version.ID)
	if !ok || strings.Contains(generated, value.Value()) || created.ValueDisclosed {
		t.Fatalf("unsafe generate response: %s", generated)
	}

	listed := invokeSecretTool(t, reg, native.SecretListTool, map[string]any{}, tctx)
	if !strings.Contains(listed, created.Secret.Reference) || strings.Contains(listed, value.Value()) {
		t.Fatalf("unsafe list response: %s", listed)
	}

	revoked := invokeSecretTool(t, reg, native.SecretRevokeTool, map[string]any{
		"reference": created.Secret.Reference,
	}, tctx)
	if !strings.Contains(revoked, `"state":"revoked"`) {
		t.Fatalf("revoke response = %s", revoked)
	}
	if _, ok := values.Get(version.ID); !ok {
		t.Fatal("revocation removed provider-screening evidence")
	}
}

func TestSecretGenerateProjectScopeRequiresPurpose(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "root-chat", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	},
		func(id string) bool { _, err := uuid.Parse(id); return err == nil },
	)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register secret tools", native.RegisterSecretCapabilityTools(reg, secretcap.NewWithStore(database, values, nil)))
	def, ok := reg.Definition(native.SecretGenerateTool)
	if !ok {
		t.Fatal("secret_generate missing")
	}
	_, err := def.Handler(context.Background(), map[string]any{
		"name": "Shared test key", "scope": "project",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID,
			SessionID:  "root-chat",
			ToolCallID: "generate-project"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SECRET_GENERATE_INVALID" {
		t.Fatalf("error = %v want SECRET_GENERATE_INVALID", err)
	}
}

func invokeSecretTool(
	t *testing.T,
	reg *tools.DefaultRegistry,
	name string,
	args map[string]any,
	tctx tools.ToolContext,
) string {
	t.Helper()
	def, ok := reg.Definition(name)
	if !ok {
		t.Fatalf("tool %q missing", name)
	}
	out, err := def.Handler(context.Background(), args, tctx)
	testutil.FailErr(t, "invoke "+name, err)
	return out
}
