package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/mcp"
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

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
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

	store, err := mcp.NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open static credential store", err)
	httpRec, ok := store.Get("remote-http")
	if !ok || httpRec.Token != "sk-mcp-secret-token-111" || httpRec.Headers["X-Custom-Auth"] != "custom-secret-header-222" || !httpRec.Migrated {
		t.Fatalf("unexpected remote-http vault record: ok=%v rec=%+v", ok, httpRec)
	}
	stdioRec, ok := store.Get("local-stdio")
	if !ok || stdioRec.Env["MCP_API_KEY"] != "stdio-secret-env-333" || !stdioRec.Migrated {
		t.Fatalf("unexpected local-stdio vault record: ok=%v rec=%+v", ok, stdioRec)
	}

	// Simulate process restart against scrubbed mcp.yaml and verify hydration.
	restarted, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime restart", err)
	testutil.FailErr(t, "Catalog.Load restart", restarted.Catalog.Load(ctx))

	httpRow, ok := restarted.Catalog.GetProvider(ctx, mcp.CallScope{}, "remote-http")
	if !ok || !httpRow.HasCredential {
		t.Fatalf("restarted remote-http missing credential flag: ok=%v row=%+v", ok, httpRow)
	}
	stdioRow, ok := restarted.Catalog.GetProvider(ctx, mcp.CallScope{}, "local-stdio")
	if !ok || stdioRow.Env == nil || (*stdioRow.Env)["MCP_API_KEY"] == "" {
		t.Fatalf("restarted local-stdio missing hydrated env mask: ok=%v row=%+v", ok, stdioRow)
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

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
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

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "initial Load", reg.Catalog.Load(ctx))

	// 1. Explicitly clear token and headers on http-prov, and clear env on stdio-prov.
	emptyTok := ""
	emptyMap := map[string]string{}
	_, err = reg.Administration.UpdateProvider(ctx, mcp.CallScope{}, "http-prov", api.UpdateMcpProviderRequest{
		Token:   &emptyTok,
		Headers: &emptyMap,
	}, "")
	testutil.FailErr(t, "clear http-prov token and headers", err)

	_, err = reg.Administration.UpdateProvider(ctx, mcp.CallScope{}, "stdio-prov", api.UpdateMcpProviderRequest{
		Env: &emptyMap,
	}, "")
	testutil.FailErr(t, "clear stdio-prov env", err)

	// Simulate an interrupted scrub / stale YAML on disk followed by process restart.
	testutil.FailErr(t, "restore stale mcp.yaml", os.WriteFile(globalPath, []byte(staleYAML), 0o600))

	restarted, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime after clear", err)
	testutil.FailErr(t, "Load after clear with stale YAML", restarted.Catalog.Load(ctx))

	httpRow, ok := restarted.Catalog.GetProvider(ctx, mcp.CallScope{}, "http-prov")
	if !ok || httpRow.HasCredential {
		t.Fatalf("stale YAML resurrected cleared HTTP credentials: ok=%v row=%+v", ok, httpRow)
	}
	stdioRow, ok := restarted.Catalog.GetProvider(ctx, mcp.CallScope{}, "stdio-prov")
	if !ok || (stdioRow.Env != nil && len(*stdioRow.Env) > 0) {
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
	store, err := mcp.NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after delete", err)
	delRec, ok := store.Get("http-prov")
	if !ok || !delRec.Deleted || delRec.HasSecrets() {
		t.Fatalf("expected deleted tombstone without secrets, got ok=%v rec=%+v", ok, delRec)
	}

	testutil.FailErr(t, "restore stale mcp.yaml after delete", os.WriteFile(globalPath, []byte(staleYAML), 0o600))
	restartedAfterDel, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime after delete", err)
	testutil.FailErr(t, "Load after delete with stale YAML", restartedAfterDel.Catalog.Load(ctx))

	httpRowAfterDel, ok := restartedAfterDel.Catalog.GetProvider(ctx, mcp.CallScope{}, "http-prov")
	if !ok || httpRowAfterDel.HasCredential {
		t.Fatalf("stale YAML resurrected deleted provider credentials: ok=%v row=%+v", ok, httpRowAfterDel)
	}
}

func TestHydrationBeforeTrustMergeBlocksProjectRepoint(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")
	projectDir := t.TempDir()

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return true })
	testutil.FailErr(t, "Load", reg.Catalog.Load(ctx))

	enabled := true
	_, err = reg.Administration.CreateProvider(ctx, mcp.CallScope{}, api.CreateMcpProviderRequest{
		Id:      "authed-user-prov",
		Url:     "https://trusted.example.com/mcp",
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

	rows := reg.Catalog.ListProviders(ctx, mcp.CallScope{ProjectDir: projectDir})
	foundRejected := false
	for _, r := range rows {
		if r.ID == "authed-user-prov" && r.Status == api.McpStatusRejected && r.LastError == mcp.RejectProjectEnableForbidden {
			foundRejected = true
		}
		if r.ID == "authed-user-prov" && r.Url != nil && strings.Contains(*r.Url, "127.0.0.1:9999") && r.Status != api.McpStatusRejected {
			t.Fatalf("project repoint of credentialed user provider was accepted: %+v", r)
		}
	}
	if !foundRejected {
		t.Fatalf("expected project repoint to be rejected with %q, got rows=%+v", mcp.RejectProjectEnableForbidden, rows)
	}
}

func TestConcurrentAdminUpdatesAndLoadsPreserveLockOrderAndUpdates(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Load", reg.Catalog.Load(ctx))

	enabled := false
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		_, err := reg.Administration.CreateProvider(ctx, mcp.CallScope{}, api.CreateMcpProviderRequest{
			Id:      id,
			Url:     "https://mcp.example.com/" + id,
			Enabled: &enabled,
			Token:   "init-" + id,
		}, "")
		testutil.FailErr(t, "CreateProvider "+id, err)
	}

	var wg sync.WaitGroup
	for i, id := range []string{"p1", "p2", "p3", "p4"} {
		wg.Add(2)
		go func(provID string, idx int) {
			defer wg.Done()
			tok := "updated-token-" + provID
			hdrs := map[string]string{"X-Idx": provID}
			_, err := reg.Administration.UpdateProvider(ctx, mcp.CallScope{}, provID, api.UpdateMcpProviderRequest{
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

	store, err := mcp.NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store", err)
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		rec, ok := store.Get(id)
		if !ok || rec.Token != "updated-token-"+id || rec.Headers["X-Idx"] != id {
			t.Fatalf("lost update for %s: ok=%v rec=%+v", id, ok, rec)
		}
	}
}
