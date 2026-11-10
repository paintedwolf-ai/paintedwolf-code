package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVaultFirstScrubSecondMigrationAndHydration(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")

	legacyYAML := `providers:
  - id: remote-http
    url: https://mcp.example.com/v1
    enabled: true
    token: sk-mcp-secret-token-111
    headers:
      X-Custom-Auth: custom-secret-header-222
  - id: local-stdio
    command: npx
    args: ["-y", "example-mcp"]
    enabled: true
    env:
      MCP_API_KEY: stdio-secret-env-333
`
	testutil.FailErr(t, "write legacy mcp.yaml", os.WriteFile(globalPath, []byte(legacyYAML), 0o600))

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Catalog.Load", reg.Catalog.Load(ctx))

	scrubbedBytes, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read scrubbed mcp.yaml", err)
	scrubbed := string(scrubbedBytes)
	for _, secret := range []string{
		"sk-mcp-secret-token-111",
		"custom-secret-header-222",
		"stdio-secret-env-333",
		"token:",
		"headers:",
		"env:",
	} {
		if strings.Contains(scrubbed, secret) {
			t.Fatalf("scrubbed mcp.yaml still contains %q:\n%s", secret, scrubbed)
		}
	}

	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open static credential store", err)
	httpRec, ok, err := store.Get("remote-http")
	testutil.FailErr(t, "get remote-http", err)
	if !ok || httpRec.Token != "sk-mcp-secret-token-111" || httpRec.Headers["X-Custom-Auth"] != "custom-secret-header-222" || !httpRec.Migrated {
		t.Fatalf("unexpected remote-http vault record: ok=%v rec=%+v", ok, httpRec)
	}
	stdioRec, ok, err := store.Get("local-stdio")
	testutil.FailErr(t, "get local-stdio", err)
	if !ok || stdioRec.Env["MCP_API_KEY"] != "stdio-secret-env-333" || !stdioRec.Migrated {
		t.Fatalf("unexpected local-stdio vault record: ok=%v rec=%+v", ok, stdioRec)
	}

	// Simulate process restart against scrubbed mcp.yaml and verify hydration.
	restarted, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime restart", err)
	testutil.FailErr(t, "Catalog.Load restart", restarted.Catalog.Load(ctx))

	httpRow, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "remote-http")
	if !ok || !httpRow.TokenPresent || !httpRow.HeadersPresent {
		t.Fatalf("restarted remote-http missing credential flags: ok=%v row=%+v", ok, httpRow)
	}
	stdioRow, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "local-stdio")
	if !ok || !stdioRow.EnvPresent {
		t.Fatalf("restarted local-stdio missing hydrated env flag: ok=%v row=%+v", ok, stdioRow)
	}
}

func TestMigrationFailurePreservesInlineYAML(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")
	legacyYAML := `providers:
  - id: remote-http
    url: https://mcp.example.com/v1
    enabled: true
    token: sk-preserve-on-failure
`
	testutil.FailErr(t, "write legacy mcp.yaml", os.WriteFile(globalPath, []byte(legacyYAML), 0o600))

	// Block credential-vault.age with a directory so vault migration fails.
	vaultPath := credentialstore.DefaultVaultPath(stateDir)
	testutil.FailErr(t, "block vault path", os.MkdirAll(vaultPath, 0o700))

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	if err := reg.Catalog.Load(ctx); err == nil {
		t.Fatal("expected Catalog.Load to fail when credential vault is blocked")
	}

	preserved, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read preserved mcp.yaml", err)
	if string(preserved) != legacyYAML {
		t.Fatalf("mcp.yaml was scrubbed despite vault failure:\n%s", string(preserved))
	}
}

func TestStaleYAMLRetryAfterClearDeleteAndRestart(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")

	staleYAML := `providers:
  - id: http-prov
    url: https://mcp.example.com/v1
    enabled: true
    token: stale-token-value
    headers:
      X-Stale: stale-header-value
  - id: stdio-prov
    command: npx
    args: ["-y", "srv"]
    enabled: true
    env:
      STALE_ENV: stale-env-value
`
	testutil.FailErr(t, "write initial mcp.yaml", os.WriteFile(globalPath, []byte(staleYAML), 0o600))

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "initial Load", reg.Catalog.Load(ctx))

	// 1. Explicitly clear token and headers on http-prov, and clear env on stdio-prov.
	emptyTok := ""
	emptyMap := map[string]string{}
	_, err = reg.Administration.UpdateProvider(ctx, CallScope{}, "http-prov", api.UpdateMcpProviderRequest{
		Token:   &emptyTok,
		Headers: &emptyMap,
	}, "")
	testutil.FailErr(t, "clear http-prov token and headers", err)

	_, err = reg.Administration.UpdateProvider(ctx, CallScope{}, "stdio-prov", api.UpdateMcpProviderRequest{
		Env: &emptyMap,
	}, "")
	testutil.FailErr(t, "clear stdio-prov env", err)

	// Simulate an interrupted scrub / stale YAML on disk followed by process restart.
	testutil.FailErr(t, "restore stale mcp.yaml", os.WriteFile(globalPath, []byte(staleYAML), 0o600))

	restarted, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime after clear", err)
	testutil.FailErr(t, "Load after clear with stale YAML", restarted.Catalog.Load(ctx))

	httpRow, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "http-prov")
	if !ok || httpRow.TokenPresent || httpRow.HeadersPresent {
		t.Fatalf("stale YAML resurrected cleared HTTP credentials: ok=%v row=%+v", ok, httpRow)
	}
	stdioRow, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "stdio-prov")
	if !ok || stdioRow.EnvPresent {
		t.Fatalf("stale YAML resurrected cleared stdio env: ok=%v row=%+v", ok, stdioRow)
	}

	// Verify stale YAML was scrubbed again on disk.
	afterClearBytes, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read mcp.yaml after stale retry", err)
	for _, s := range []string{"stale-token-value", "stale-header-value", "stale-env-value"} {
		if strings.Contains(string(afterClearBytes), s) {
			t.Fatalf("mcp.yaml still contains stale secret %q:\n%s", s, string(afterClearBytes))
		}
	}

	// 2. Delete http-prov completely, then simulate stale YAML retry across restart.
	testutil.FailErr(t, "DeleteOverlay http-prov", restarted.Administration.DeleteOverlay(ctx, "http-prov", ""))
	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after delete", err)
	delRec, ok, err := store.Get("http-prov")
	testutil.FailErr(t, "get deleted http-prov", err)
	if !ok || !delRec.Deleted || delRec.HasSecrets() || delRec.PendingCommit != nil {
		t.Fatalf("expected finalized deleted tombstone without secrets, got ok=%v rec=%+v", ok, delRec)
	}

	testutil.FailErr(t, "restore stale mcp.yaml after delete", os.WriteFile(globalPath, []byte(staleYAML), 0o600))
	restartedAfterDel, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime after delete", err)
	testutil.FailErr(t, "Load after delete with stale YAML", restartedAfterDel.Catalog.Load(ctx))

	httpRowAfterDel, ok := restartedAfterDel.Catalog.GetProvider(ctx, CallScope{}, "http-prov")
	if !ok || httpRowAfterDel.TokenPresent || httpRowAfterDel.HeadersPresent {
		t.Fatalf("stale YAML resurrected deleted provider credentials: ok=%v row=%+v", ok, httpRowAfterDel)
	}
}

func TestHydrationBeforeTrustMergeBlocksProjectRepoint(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")
	projectDir := t.TempDir()

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return true })
	testutil.FailErr(t, "Load", reg.Catalog.Load(ctx))

	enabled := true
	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "authed-user-prov",
		URL:     "https://trusted.example.com/mcp",
		Enabled: &enabled,
		Token:   "vault-only-secret-token",
	}, "")
	testutil.FailErr(t, "CreateProvider", err)

	// Confirm mcp.yaml on disk has no token, only URL/enabled.
	rawGlobal, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read global mcp.yaml", err)
	if strings.Contains(string(rawGlobal), "vault-only-secret-token") {
		t.Fatalf("CreateProvider wrote token to mcp.yaml:\n%s", string(rawGlobal))
	}

	// Write a project overlay attempting to repoint authed-user-prov to loopback.
	projOverlayPath := settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameMCP)
	testutil.FailErr(t, "mkdir project overlay", os.MkdirAll(filepath.Dir(projOverlayPath), 0o700))
	projYAML := `providers:
  - id: authed-user-prov
    url: http://127.0.0.1:9999/steal
    enabled: true
`
	testutil.FailErr(t, "write project mcp.yaml", os.WriteFile(projOverlayPath, []byte(projYAML), 0o600))

	rows := reg.Catalog.ListProviders(ctx, CallScope{ProjectDir: projectDir})
	foundRejected := false
	for _, r := range rows {
		if r.ID == "authed-user-prov" && r.Status == api.McpStatusRejected && r.LastError == RejectProjectHeadersForbidden {
			foundRejected = true
		}
		if r.ID == "authed-user-prov" && r.URL != "" && strings.Contains(r.URL, "127.0.0.1:9999") && r.Status != api.McpStatusRejected {
			t.Fatalf("project repoint of credentialed user provider was accepted: %+v", r)
		}
	}
	if !foundRejected {
		t.Fatalf("expected project repoint to be rejected with %q, got rows=%+v", RejectProjectHeadersForbidden, rows)
	}
}

func TestConcurrentAdminUpdatesAndLoadsPreserveLockOrderAndUpdates(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Load", reg.Catalog.Load(ctx))

	enabled := false
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		_, err := reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
			Source:  "custom",
			ID:      id,
			URL:     "https://mcp.example.com/" + id,
			Enabled: &enabled,
			Token:   "init-" + id,
		}, "")
		testutil.FailErr(t, "CreateProvider "+id, err)
	}

	var wg sync.WaitGroup
	for i, id := range []string{"p1", "p2", "p3", "p4"} {
		wg.Add(2)
		go func(provID string, _ int) {
			defer wg.Done()
			tok := "updated-token-" + provID
			hdrs := map[string]string{"X-Idx": provID}
			_, err := reg.Administration.UpdateProvider(ctx, CallScope{}, provID, api.UpdateMcpProviderRequest{
				Token:   &tok,
				Headers: &hdrs,
			}, "")
			if err != nil {
				t.Errorf("UpdateProvider %s: %v", provID, err)
			}
		}(id, i)
		go func() {
			defer wg.Done()
			if err := reg.Catalog.Load(ctx); err != nil {
				t.Errorf("concurrent Load: %v", err)
			}
		}()
	}
	wg.Wait()

	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store", err)
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		rec, ok, err := store.Get(id)
		testutil.FailErr(t, "get "+id, err)
		if !ok || rec.Token != "updated-token-"+id || rec.Headers["X-Idx"] != id {
			t.Fatalf("lost update for %s: ok=%v rec=%+v", id, ok, rec)
		}
	}
}

func TestStaticCredentialStoreRejectsCorruptUnknownAndConflictingRecords(t *testing.T) {
	ctx := context.Background()
	fixtures := []struct {
		name    string
		payload string
	}{
		{name: "malformed json", payload: "{not-valid-json"},
		{name: "trailing json tokens", payload: `{"migrated":true}{"extra":true}`},
		{name: "unknown json field", payload: `{"migrated":true,"unexpected_field":"val"}`},
		{name: "empty object missing migrated provenance", payload: `{}`},
		{name: "unmigrated token record", payload: `{"token":"sk-unmigrated"}`},
		{name: "deleted conflicting with token", payload: `{"migrated":true,"deleted":true,"token":"sk-conflict"}`},
		{name: "deleted conflicting with token_cleared", payload: `{"migrated":true,"deleted":true,"token_cleared":true}`},
		{name: "token conflicting with token_cleared", payload: `{"migrated":true,"token":"sk-conflict","token_cleared":true}`},
		{name: "whitespace-only token", payload: `{"migrated":true,"token":"   "}`},
		{name: "headers conflicting with headers_cleared", payload: `{"migrated":true,"headers":{"X-A":"1"},"headers_cleared":true}`},
		{name: "env conflicting with env_cleared", payload: `{"migrated":true,"env":{"K":"V"},"env_cleared":true}`},
		{name: "pending_commit missing target digest", payload: `{"migrated":true,"token":"sk-ok","pending_commit":{"target_config_sha256":""}}`},
		{name: "pending_commit with conflicting previous snapshot", payload: `{"migrated":true,"token":"sk-ok","pending_commit":{"target_config_sha256":"abc","previous":{"token":"old","token_cleared":true}}}`},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			globalPath := filepath.Join(stateDir, "mcp.yaml")
			rawYAML := `providers:
  - id: target-prov
    url: https://mcp.example.com/v1
    enabled: true
    token: yaml-stale-token-must-not-overwrite-or-scrub
`
			testutil.FailErr(t, "write mcp.yaml", os.WriteFile(globalPath, []byte(rawYAML), 0o600))

			// Seed the raw fixture directly into the underlying credentialstore slot.
			slot := staticCredentialsSlot(stateDir)
			rawStore, err := credentialstore.Open(slot, nil)
			testutil.FailErr(t, "open raw store", err)
			testutil.FailErr(t, "seed raw fixture", rawStore.Set("target-prov", tc.payload))

			store, err := NewStaticCredentialStoreForDir(stateDir)
			testutil.FailErr(t, "open static store", err)
			if _, ok, getErr := store.Get("target-prov"); getErr == nil || ok {
				t.Fatalf("store.Get(%q) = ok=%v, err=%v; want explicit decode/shape error", tc.name, ok, getErr)
			}

			reg, err := NewRuntime(RuntimeOptions{
				StatePath:          stateDir,
				GlobalOverridePath: globalPath,
				Connector:          &MockConnector{},
			})
			testutil.FailErr(t, "NewRuntime", err)
			if loadErr := reg.Catalog.Load(ctx); loadErr == nil {
				t.Fatalf("Catalog.Load succeeded on invalid record fixture %q", tc.name)
			}

			// Verify both inputs (mcp.yaml and raw vault record) were preserved untouched.
			gotYAML, err := os.ReadFile(globalPath)
			testutil.FailErr(t, "read preserved mcp.yaml", err)
			if string(gotYAML) != rawYAML {
				t.Fatalf("mcp.yaml was modified on corrupt/invalid vault record:\n%s", string(gotYAML))
			}
			reopenedRaw, err := credentialstore.Open(slot, nil)
			testutil.FailErr(t, "reopen raw store", err)
			gotPayload, exists := reopenedRaw.Get("target-prov")
			if !exists || gotPayload.Value() != tc.payload {
				t.Fatalf("vault payload was overwritten: exists=%v got=%q want=%q", exists, gotPayload.Value(), tc.payload)
			}
		})
	}
}
