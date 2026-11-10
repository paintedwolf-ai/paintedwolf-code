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
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreflightValidationRejectsCorruptRecordBeforeMutatingValidPendingRecord(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")
	rawYAML := `providers:
  - id: alpha
    url: https://mcp.example.com/alpha
    enabled: true
`
	testutil.FailErr(t, "write mcp.yaml", os.WriteFile(globalPath, []byte(rawYAML), 0o600))

	store, err := NewStaticCredentialStoreForDir(stateDir)
	testutil.FailErr(t, "open static store", err)
	prevSnap := StaticCredentialSnapshot{Token: "alpha-prior-token"}
	testutil.FailErr(t, "seed valid pending alpha", store.PutVerified("alpha", StaticCredentialRecord{
		Token:    "alpha-uncommitted-token",
		Migrated: true,
		PendingCommit: &PendingCredentialCommit{
			TargetConfigSHA256: "uncommitted-digest",
			Committed:          false,
			Previous:           &prevSnap,
		},
	}))

	slot := staticCredentialsSlot(stateDir)
	rawStore, err := credentialstore.Open(slot, nil)
	testutil.FailErr(t, "open raw store", err)
	alphaBefore, ok := rawStore.Get("alpha")
	if !ok {
		t.Fatal("missing alpha raw record")
	}
	corruptOmega := `{"token":"omega-unmigrated-corrupt"}`
	testutil.FailErr(t, "seed corrupt omega sorting after alpha", rawStore.Set("omega", corruptOmega))

	reg, err := NewRuntime(RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	loadErr := reg.Catalog.Load(ctx)
	if !errors.Is(loadErr, credentialstore.ErrVaultCorrupt) {
		t.Fatalf("Catalog.Load err = %v, want ErrVaultCorrupt", loadErr)
	}

	reopenedRaw, err := credentialstore.Open(slot, nil)
	testutil.FailErr(t, "reopen raw store", err)
	alphaAfter, ok := reopenedRaw.Get("alpha")
	if !ok || alphaAfter.Value() != alphaBefore.Value() {
		t.Fatalf("alpha pending record was mutated before omega preflight failure:\ngot:  %s\nwant: %s", alphaAfter.Value(), alphaBefore.Value())
	}
	omegaAfter, ok := reopenedRaw.Get("omega")
	if !ok || omegaAfter.Value() != corruptOmega {
		t.Fatalf("omega record was mutated: %q", omegaAfter.Value())
	}
	gotYAML, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read mcp.yaml", err)
	if string(gotYAML) != rawYAML {
		t.Fatalf("mcp.yaml was modified during failed preflight:\n%s", string(gotYAML))
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

	_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "existing-prov",
		URL:     "https://mcp.example.com/existing",
		Enabled: &enabled,
		Token:   "working-token-v1",
	}, "")
	testutil.FailErr(t, "CreateProvider existing-prov", err)

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
}

func TestPendingCommitRecoveryAcrossCrashValidationAndRollbackBoundaries(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "mcp.yaml")

	newReg := func() *Runtime {
		r, err := NewRuntime(RuntimeOptions{
			StatePath:          stateDir,
			GlobalOverridePath: globalPath,
			Connector:          &MockConnector{},
		})
		testutil.FailErr(t, "NewRuntime", err)
		testutil.FailErr(t, "Load", r.Catalog.Load(ctx))
		return r
	}
	assertVaultToken := func(id, want string) {
		t.Helper()
		s, err := NewStaticCredentialStoreForDir(stateDir)
		testutil.FailErr(t, "open store", err)
		rec, ok, err := s.Get(id)
		testutil.FailErr(t, "get "+id, err)
		if want == "" {
			if ok && !rec.Deleted {
				t.Fatalf("expected %s to be absent or deleted, got %+v", id, rec)
			}
			return
		}
		if !ok || rec.Token != want || rec.PendingCommit != nil {
			t.Fatalf("%s vault record = %+v (ok=%v), want token=%q with nil PendingCommit", id, rec, ok, want)
		}
	}

	reg := newReg()
	enabled := false
	_, err := reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "p-survive",
		URL:     "https://mcp.example.com/survive",
		Enabled: &enabled,
		Token:   "committed-token-v1",
	}, "")
	testutil.FailErr(t, "CreateProvider p-survive", err)

	// 1. Credential-only UpdateProvider crashing at "after_pending_write" (before mcp.yaml write / Validate).
	// Even though scrubbed mcp.yaml digest is identical to the on-disk file, Committed=false must roll back.
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_pending_write" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after pending write")
		}
		return nil
	}
	credOnlyTok := "uncommitted-credential-only-v2"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{
		Token: &credOnlyTok,
	}, ""); err == nil {
		t.Fatal("expected simulated crash on credential-only update")
	}
	reg = newReg()
	assertVaultToken("p-survive", "committed-token-v1")

	// 2. Invalid create and invalid update where Validate fails after Publish, crashing at "before_rollback".
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "before_rollback" {
			return errors.New("simulated crash before rollback completion")
		}
		return nil
	}
	badRecipe := "nonexistent-recipe"
	badTok := "orphan-rejected-create-token"
	if _, err := reg.Administration.persistNewOverlay(ctx, CallScope{}, "rejected-create", MCPProviderOverlay{
		ID:     "rejected-create",
		Recipe: &badRecipe,
		Token:  &badTok,
	}, ""); err == nil {
		t.Fatal("expected persistNewOverlay with unknown recipe to fail Validate")
	}
	rawYAML, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read mid-rollback mcp.yaml", err)
	if !strings.Contains(string(rawYAML), "rejected-create") {
		t.Fatalf("expected simulated crash before rollback to leave rejected-create in mcp.yaml:\n%s", string(rawYAML))
	}
	reg = newReg()
	assertVaultToken("rejected-create", "")
	rawYAMLAfter, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read recovered mcp.yaml", err)
	if strings.Contains(string(rawYAMLAfter), "rejected-create") {
		t.Fatalf("expected startup recovery to remove uncommitted rejected-create from mcp.yaml:\n%s", string(rawYAMLAfter))
	}

	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "before_rollback" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash before update rollback")
		}
		return nil
	}
	emptyURL := ""
	rejectedUpdateTok := "orphan-rejected-update-token"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{
		URL:   &emptyURL,
		Token: &rejectedUpdateTok,
	}, ""); err == nil {
		t.Fatal("expected UpdateProvider clearing transport to fail Validate")
	}
	reg = newReg()
	assertVaultToken("p-survive", "committed-token-v1")
	row, ok := reg.Catalog.GetProvider(ctx, CallScope{}, "p-survive")
	if !ok || row.Status == api.McpStatusRejected || row.URL != "https://mcp.example.com/survive" {
		t.Fatalf("p-survive after failed update crash recovery = %+v (ok=%v)", row, ok)
	}

	// 3. Crash after config publication and Committed=true mark ("after_commit_mark"), before finalization.
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_commit_mark" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after commit mark")
		}
		return nil
	}
	tokV2 := "committed-token-v2"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{
		Token: &tokV2,
	}, ""); err == nil {
		t.Fatal("expected simulated crash at after_commit_mark")
	}
	reg = newReg()
	assertVaultToken("p-survive", "committed-token-v2")

	// 4. Crash after finalization ("after_finalize") and DeleteOverlay crash boundaries.
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_finalize" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after finalize")
		}
		return nil
	}
	tokV3 := "committed-token-v3"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{Token: &tokV3}, ""); err == nil {
		t.Fatal("expected simulated crash at after_finalize")
	}
	reg = newReg()
	assertVaultToken("p-survive", "committed-token-v3")

	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_pending_write" && op == "delete" && id == "p-survive" {
			return errors.New("simulated crash during delete before config write")
		}
		return nil
	}
	if err := reg.Administration.DeleteOverlay(ctx, "p-survive", ""); err == nil {
		t.Fatal("expected simulated crash on delete")
	}
	reg = newReg()
	assertVaultToken("p-survive", "committed-token-v3")
}
