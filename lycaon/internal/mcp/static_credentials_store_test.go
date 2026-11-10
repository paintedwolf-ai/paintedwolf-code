package mcp

import (
	"context"
	"errors"
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

func TestAdminRollbackIsSerializedInsideCatalogTransactionAndSurfacesErrors(t *testing.T) {
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
	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "p-serial",
		URL:     "https://mcp.example.com/serial",
		Enabled: &enabled,
		Token:   "initial-token",
	}, "")
	testutil.FailErr(t, "CreateProvider", err)

	// Deterministic interleaving: Writer 1 stages "failed-writer-token" in the vault,
	// signals Writer 2 to start UpdateProvider("concurrent-winner-token"), and then
	// Writer 1 fails its config write hook. Because vault rollback runs inside
	// UpdateFile's serialized lock, Writer 2 cannot enter Apply until Writer 1 has
	// rolled back "initial-token", so Writer 2's "concurrent-winner-token" is never
	// overwritten by Writer 1's rollback.
	writer1Staged := make(chan struct{})
	writer2Queued := make(chan struct{})
	var hookOnce sync.Once
	reg.Administration.beforeConfigWrite = func(op, providerID string) error {
		if op == "update" && providerID == "p-serial" {
			failed := false
			hookOnce.Do(func() {
				failed = true
				close(writer1Staged)
				<-writer2Queued
			})
			if failed {
				return errors.New("simulated config write failure after vault stage")
			}
		}
		return nil
	}

	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		tok := "failed-writer-token"
		_, err1 = reg.Administration.UpdateProvider(ctx, CallScope{}, "p-serial", api.UpdateMcpProviderRequest{
			Token: &tok,
		}, "")
	}()
	go func() {
		defer wg.Done()
		<-writer1Staged
		close(writer2Queued)
		tok := "concurrent-winner-token"
		_, err2 = reg.Administration.UpdateProvider(ctx, CallScope{}, "p-serial", api.UpdateMcpProviderRequest{
			Token: &tok,
		}, "")
	}()
	wg.Wait()
	reg.Administration.beforeConfigWrite = nil

	if err1 == nil {
		t.Fatal("expected Writer 1 UpdateProvider to fail")
	}
	testutil.FailErr(t, "Writer 2 UpdateProvider", err2)

	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store", err)
	rec, ok, err := store.Get("p-serial")
	testutil.FailErr(t, "get p-serial", err)
	if !ok || rec.Token != "concurrent-winner-token" || rec.PendingCommit != nil {
		t.Fatalf("vault state after concurrent rollback/commit = %+v, want concurrent-winner-token", rec)
	}

	// Verify runtime publication after restart.
	restarted, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime restart", err)
	testutil.FailErr(t, "Load restart", restarted.Catalog.Load(ctx))
	row, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "p-serial")
	if !ok || !row.TokenPresent {
		t.Fatalf("restarted row = %+v, ok=%v", row, ok)
	}
}

func TestCommittedButVerificationFailedBoundaryForCreateUpdateDelete(t *testing.T) {
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

	// 1. CreateProvider where s.store.Set commits to vault and verifyDurable fails.
	reg.Administration.openStaticCredentials = func(dir string) (*StaticCredentialStore, error) {
		s, err := NewStaticCredentialStoreForDir(dir)
		if err != nil {
			return nil, err
		}
		s.verifyDurable = func(credentialstore.Slot, string, string) error {
			return errors.New("injected durable verification failure on create")
		}
		return s, nil
	}
	enabled := false
	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "create-verify-fail",
		URL:     "https://mcp.example.com/create-fail",
		Enabled: &enabled,
		Token:   "unverified-create-token",
	}, "")
	if err == nil {
		t.Fatal("expected CreateProvider to fail when durable verification fails")
	}
	reg.Administration.openStaticCredentials = nil

	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after failed create", err)
	if _, ok, err := store.Get("create-verify-fail"); err != nil || ok {
		t.Fatalf("failed create left vault record: ok=%v err=%v", ok, err)
	}

	// Now create a healthy provider so we can test UpdateProvider and DeleteOverlay verification failures.
	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "existing-prov",
		URL:     "https://mcp.example.com/existing",
		Enabled: &enabled,
		Token:   "working-token-v1",
	}, "")
	testutil.FailErr(t, "CreateProvider existing-prov", err)

	// 2. UpdateProvider where s.store.Set commits to vault and verifyDurable fails.
	reg.Administration.openStaticCredentials = func(dir string) (*StaticCredentialStore, error) {
		s, err := NewStaticCredentialStoreForDir(dir)
		if err != nil {
			return nil, err
		}
		s.verifyDurable = func(credentialstore.Slot, string, string) error {
			return errors.New("injected durable verification failure on update")
		}
		return s, nil
	}
	newTok := "unverified-token-v2"
	_, err = reg.Administration.UpdateProvider(ctx, CallScope{}, "existing-prov", api.UpdateMcpProviderRequest{
		Token: &newTok,
	}, "")
	if err == nil {
		t.Fatal("expected UpdateProvider to fail when durable verification fails")
	}
	reg.Administration.openStaticCredentials = nil

	store, err = NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after failed update", err)
	rec, ok, err := store.Get("existing-prov")
	testutil.FailErr(t, "get existing-prov after failed update", err)
	if !ok || rec.Token != "working-token-v1" || rec.PendingCommit != nil {
		t.Fatalf("failed update did not preserve working-token-v1: ok=%v rec=%+v", ok, rec)
	}

	// 3. DeleteOverlay where s.store.Set commits Deleted=true and verifyDurable fails.
	reg.Administration.openStaticCredentials = func(dir string) (*StaticCredentialStore, error) {
		s, err := NewStaticCredentialStoreForDir(dir)
		if err != nil {
			return nil, err
		}
		s.verifyDurable = func(credentialstore.Slot, string, string) error {
			return errors.New("injected durable verification failure on delete")
		}
		return s, nil
	}
	if err := reg.Administration.DeleteOverlay(ctx, "existing-prov", ""); err == nil {
		t.Fatal("expected DeleteOverlay to fail when durable verification fails")
	}
	reg.Administration.openStaticCredentials = nil

	store, err = NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after failed delete", err)
	rec, ok, err = store.Get("existing-prov")
	testutil.FailErr(t, "get existing-prov after failed delete", err)
	if !ok || rec.Deleted || rec.Token != "working-token-v1" {
		t.Fatalf("failed delete lost working-token-v1: ok=%v rec=%+v", ok, rec)
	}

	// Verify fresh runtime after restart still sees existing-prov with its credential.
	restarted, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime after failed delete", err)
	testutil.FailErr(t, "Load after failed delete", restarted.Catalog.Load(ctx))
	row, ok := restarted.Catalog.GetProvider(ctx, CallScope{}, "existing-prov")
	if !ok || !row.TokenPresent {
		t.Fatalf("restarted existing-prov lost credential: ok=%v row=%+v", ok, row)
	}
}

func TestCrashBetweenVaultStageAndConfigWriteRecoversOnRestart(t *testing.T) {
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

	enabled := true
	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "crash-survive",
		URL:     "https://mcp.example.com/survive",
		Enabled: &enabled,
		Token:   "committed-token-before-crash",
	}, "")
	testutil.FailErr(t, "CreateProvider crash-survive", err)

	// 1. Simulate a hard process crash during CreateProvider("crashed-create"):
	// PutVerified staged the new record with PendingCommit{TargetConfigSHA256: "unreached-digest", Previous: nil},
	// and the process died before mcp.yaml was written (so Rollback never ran).
	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "stage crashed create", store.PutVerified("crashed-create", StaticCredentialRecord{
		Token:    "orphan-create-token",
		Migrated: true,
		PendingCommit: &PendingCredentialCommit{
			TargetConfigSHA256: "unreached-create-digest",
			Previous:           nil,
		},
	}))

	// 2. Simulate a hard process crash during UpdateProvider("crash-survive"):
	// PutVerified staged a new token with Previous pointing to "committed-token-before-crash",
	// and the process died before mcp.yaml was updated.
	prevSnap := StaticCredentialSnapshot{Token: "committed-token-before-crash"}
	testutil.FailErr(t, "stage crashed update", store.PutVerified("crash-survive", StaticCredentialRecord{
		Token:    "uncommitted-update-token",
		Migrated: true,
		PendingCommit: &PendingCredentialCommit{
			TargetConfigSHA256: "unreached-update-digest",
			Previous:           &prevSnap,
		},
	}))

	// Restart runtime and run Catalog.Load: both crashed operations must recover automatically.
	restarted1, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime restart 1", err)
	testutil.FailErr(t, "Load restart 1", restarted1.Catalog.Load(ctx))

	storeAfter1, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after restart 1", err)
	if _, ok, err := storeAfter1.Get("crashed-create"); err != nil || ok {
		t.Fatalf("crashed create record was not removed on restart: ok=%v err=%v", ok, err)
	}
	recoveredUpdate, ok, err := storeAfter1.Get("crash-survive")
	testutil.FailErr(t, "get crash-survive after restart 1", err)
	if !ok || recoveredUpdate.Token != "committed-token-before-crash" || recoveredUpdate.PendingCommit != nil {
		t.Fatalf("crashed update did not recover committed-token-before-crash: ok=%v rec=%+v", ok, recoveredUpdate)
	}

	// 3. Simulate a hard process crash during DeleteOverlay("crash-survive"):
	// PutVerified staged Deleted=true with Previous pointing to "committed-token-before-crash",
	// and the process died before mcp.yaml removed "crash-survive".
	testutil.FailErr(t, "stage crashed delete", storeAfter1.PutVerified("crash-survive", StaticCredentialRecord{
		Migrated: true,
		Deleted:  true,
		PendingCommit: &PendingCredentialCommit{
			TargetConfigSHA256: "unreached-delete-digest",
			Previous:           &prevSnap,
		},
	}))

	restarted2, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime restart 2", err)
	testutil.FailErr(t, "Load restart 2", restarted2.Catalog.Load(ctx))

	storeAfter2, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open store after restart 2", err)
	recoveredDel, ok, err := storeAfter2.Get("crash-survive")
	testutil.FailErr(t, "get crash-survive after restart 2", err)
	if !ok || recoveredDel.Deleted || recoveredDel.Token != "committed-token-before-crash" || recoveredDel.PendingCommit != nil {
		t.Fatalf("crashed delete lost committed-token-before-crash: ok=%v rec=%+v", ok, recoveredDel)
	}
	row, ok := restarted2.Catalog.GetProvider(ctx, CallScope{}, "crash-survive")
	if !ok || !row.TokenPresent {
		t.Fatalf("restarted2 crash-survive lost credential after crashed delete recovery: ok=%v row=%+v", ok, row)
	}
}
