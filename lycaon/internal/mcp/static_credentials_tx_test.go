package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	assertProviderAndToken := func(id, wantURL, wantToken string) {
		t.Helper()
		s, err := NewStaticCredentialStoreForDir(stateDir)
		testutil.FailErr(t, "open store", err)
		rec, ok, err := s.Get(id)
		testutil.FailErr(t, "get "+id, err)
		if wantToken == "" {
			if ok && !rec.Deleted {
				t.Fatalf("expected %s to be absent or deleted, got %+v", id, rec)
			}
			return
		}
		if !ok || rec.Token != wantToken || rec.PendingCommit != nil {
			t.Fatalf("%s vault record = %+v (ok=%v), want token=%q with nil PendingCommit", id, rec, ok, wantToken)
		}
		cfg, err := LoadUserMCPConfig(globalPath)
		testutil.FailErr(t, "load mcp.yaml", err)
		ov, found := findProviderOverlayRow(cfg, id)
		if !found || ov.URL == nil || *ov.URL != wantURL {
			t.Fatalf("%s mcp.yaml overlay = %+v (found=%v), want URL=%q", id, ov, found, wantURL)
		}
	}

	reg := newReg()
	enabled := false
	baseURL := "https://mcp.example.com/survive"
	_, err := reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      "p-survive",
		URL:     baseURL,
		Enabled: &enabled,
		Token:   "committed-token-v1",
	}, "")
	testutil.FailErr(t, "CreateProvider p-survive", err)

	// 1. Credential-only UpdateProvider crashing at "after_pending_write" (before mcp.yaml write).
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
	assertProviderAndToken("p-survive", baseURL, "committed-token-v1")

	// 2. Combined URL + token UpdateProvider crashing at "after_config_write" (after mcp.yaml is
	// durably written with newURL, before Publish/Validate/commit), followed by recovery faults
	// before and after writeOrRemoveUserMCPConfig during startup recovery.
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_config_write" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after config write")
		}
		return nil
	}
	uncommittedURL := "https://mcp.example.com/uncommitted-new-host"
	uncommittedTok := "uncommitted-new-token"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{
		URL:   &uncommittedURL,
		Token: &uncommittedTok,
	}, ""); err == nil {
		t.Fatal("expected simulated crash at after_config_write")
	}
	midCrashYAML, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read mid-crash mcp.yaml", err)
	if !strings.Contains(string(midCrashYAML), uncommittedURL) {
		t.Fatalf("expected after_config_write crash to leave %q on disk:\n%s", uncommittedURL, string(midCrashYAML))
	}

	// 2a. Recovery fails at "before_recovery_config_write": PendingCommit must remain intact in vault.
	rFail1, err := NewRuntime(RuntimeOptions{StatePath: stateDir, GlobalOverridePath: globalPath, Connector: &MockConnector{}})
	testutil.FailErr(t, "NewRuntime rFail1", err)
	rFail1.Catalog.recoveryHook = func(stage, _ string) error {
		if stage == "before_recovery_config_write" {
			return errors.New("simulated disk error restoring mcp.yaml during recovery")
		}
		return nil
	}
	if err := rFail1.Catalog.Load(ctx); err == nil {
		t.Fatal("expected Catalog.Load to fail when before_recovery_config_write fails")
	}

	// 2b. Recovery writes restored mcp.yaml then crashes at "after_recovery_config_write_before_vault_restore".
	rFail2, err := NewRuntime(RuntimeOptions{StatePath: stateDir, GlobalOverridePath: globalPath, Connector: &MockConnector{}})
	testutil.FailErr(t, "NewRuntime rFail2", err)
	rFail2.Catalog.recoveryHook = func(stage, _ string) error {
		if stage == "after_recovery_config_write_before_vault_restore" {
			return errors.New("simulated crash after recovery config write before vault restore")
		}
		return nil
	}
	if err := rFail2.Catalog.Load(ctx); err == nil {
		t.Fatal("expected Catalog.Load to fail at after_recovery_config_write_before_vault_restore")
	}

	// 2c. Subsequent restart completes vault restoration coherently (old URL + old token).
	reg = newReg()
	assertProviderAndToken("p-survive", baseURL, "committed-token-v1")

	// 3. Invalid create and invalid update crashing at "before_file_rollback" and "after_file_rollback_before_cred_rollback".
	reg.Administration.crashHook = func(stage, _, _ string) error {
		if stage == "before_file_rollback" {
			return errors.New("simulated crash before file rollback")
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
		t.Fatalf("expected crash before file rollback to leave rejected-create in mcp.yaml:\n%s", string(rawYAML))
	}
	reg = newReg()
	assertProviderAndToken("rejected-create", "", "")
	rawYAMLAfter, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read recovered mcp.yaml", err)
	if strings.Contains(string(rawYAMLAfter), "rejected-create") {
		t.Fatalf("expected startup recovery to remove uncommitted rejected-create from mcp.yaml:\n%s", string(rawYAMLAfter))
	}

	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_file_rollback_before_cred_rollback" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after file rollback before cred rollback")
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
	assertProviderAndToken("p-survive", baseURL, "committed-token-v1")

	// 4. Crash after config publication and Committed=true mark ("after_commit_mark") and "after_finalize".
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_commit_mark" && op == "update" && id == "p-survive" {
			return errors.New("simulated crash after commit mark")
		}
		return nil
	}
	urlV2 := "https://mcp.example.com/survive-v2"
	tokV2 := "committed-token-v2"
	if _, err := reg.Administration.UpdateProvider(ctx, CallScope{}, "p-survive", api.UpdateMcpProviderRequest{
		URL:   &urlV2,
		Token: &tokV2,
	}, ""); err == nil {
		t.Fatal("expected simulated crash at after_commit_mark")
	}
	reg = newReg()
	assertProviderAndToken("p-survive", urlV2, "committed-token-v2")

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
	assertProviderAndToken("p-survive", urlV2, "committed-token-v3")

	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_config_write" && op == "delete" && id == "p-survive" {
			return errors.New("simulated crash during delete after config write")
		}
		return nil
	}
	if err := reg.Administration.DeleteOverlay(ctx, "p-survive", ""); err == nil {
		t.Fatal("expected simulated crash on delete")
	}
	reg = newReg()
	assertProviderAndToken("p-survive", urlV2, "committed-token-v3")
}

func TestSubprocessCrashAtPersistentPrefixBoundaries(t *testing.T) {
	ctx := context.Background()
	if stage := os.Getenv("PW_MCP_CRASH_SUBPROCESS_STAGE"); stage != "" {
		stateDir := os.Getenv("PW_MCP_CRASH_STATE_DIR")
		globalPath := filepath.Join(stateDir, "mcp.yaml")
		r, err := NewRuntime(RuntimeOptions{
			StatePath:          stateDir,
			GlobalOverridePath: globalPath,
			Connector:          &MockConnector{},
		})
		testutil.FailErr(t, "subprocess NewRuntime", err)
		if stage == "after_recovery_config_write_before_vault_restore" {
			r.Catalog.recoveryHook = func(hookStage, _ string) error {
				if hookStage == stage {
					os.Exit(42)
				}
				return nil
			}
			_ = r.Catalog.Load(ctx)
			t.Fatalf("subprocess did not exit at recovery stage %q", stage)
		}
		testutil.FailErr(t, "subprocess Load", r.Catalog.Load(ctx))
		r.Administration.crashHook = func(hookStage, _, _ string) error {
			if hookStage == stage {
				os.Exit(42)
			}
			return nil
		}
		if stage == "after_file_rollback_before_cred_rollback" {
			emptyURL := ""
			badTok := "subprocess-rejected-tok"
			_, _ = r.Administration.UpdateProvider(ctx, CallScope{}, "p-sub", api.UpdateMcpProviderRequest{
				URL:   &emptyURL,
				Token: &badTok,
			}, "")
		} else {
			nextURL := "https://mcp.example.com/sub-next"
			nextTok := "subprocess-next-tok"
			_, _ = r.Administration.UpdateProvider(ctx, CallScope{}, "p-sub", api.UpdateMcpProviderRequest{
				URL:   &nextURL,
				Token: &nextTok,
			}, "")
		}
		t.Fatalf("subprocess did not exit at stage %q", stage)
	}

	runChildExit42 := func(stateDir, stage string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashAtPersistentPrefixBoundaries$")
		cmd.Env = append(os.Environ(),
			"PW_MCP_CRASH_SUBPROCESS_STAGE="+stage,
			"PW_MCP_CRASH_STATE_DIR="+stateDir,
		)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
			t.Fatalf("stage %s child err = %v, want exit code 42", stage, err)
		}
	}

	for _, tc := range []struct {
		stage   string
		wantURL string
		wantTok string
	}{
		{stage: "after_pending_write", wantURL: "https://mcp.example.com/sub-init", wantTok: "subprocess-init-tok"},
		{stage: "after_config_write", wantURL: "https://mcp.example.com/sub-init", wantTok: "subprocess-init-tok"},
		{stage: "after_file_rollback_before_cred_rollback", wantURL: "https://mcp.example.com/sub-init", wantTok: "subprocess-init-tok"},
		{stage: "after_commit_mark", wantURL: "https://mcp.example.com/sub-next", wantTok: "subprocess-next-tok"},
	} {
		stateDir := t.TempDir()
		globalPath := filepath.Join(stateDir, "mcp.yaml")
		reg, err := NewRuntime(RuntimeOptions{StatePath: stateDir, GlobalOverridePath: globalPath, Connector: &MockConnector{}})
		testutil.FailErr(t, "NewRuntime", err)
		testutil.FailErr(t, "Load", reg.Catalog.Load(ctx))
		enabled := false
		_, err = reg.Administration.CreateProvider(ctx, CallScope{}, api.CreateMcpProviderRequest{
			Source:  "custom",
			ID:      "p-sub",
			URL:     "https://mcp.example.com/sub-init",
			Enabled: &enabled,
			Token:   "subprocess-init-tok",
		}, "")
		testutil.FailErr(t, "CreateProvider p-sub", err)

		runChildExit42(stateDir, tc.stage)
		if tc.stage == "after_config_write" {
			// Also kill a recovery child right after it restores mcp.yaml before RestoreSnapshot.
			runChildExit42(stateDir, "after_recovery_config_write_before_vault_restore")
		}

		recovered, err := NewRuntime(RuntimeOptions{StatePath: stateDir, GlobalOverridePath: globalPath, Connector: &MockConnector{}})
		testutil.FailErr(t, "NewRuntime recovered", err)
		testutil.FailErr(t, "Load recovered", recovered.Catalog.Load(ctx))

		row, ok := recovered.Catalog.GetProvider(ctx, CallScope{}, "p-sub")
		if !ok || row.URL != tc.wantURL {
			t.Fatalf("stage %s recovered provider = %+v (ok=%v), want URL=%q", tc.stage, row, ok, tc.wantURL)
		}
		store, err := NewStaticCredentialStoreForDir(stateDir)
		testutil.FailErr(t, "open store", err)
		rec, ok, err := store.Get("p-sub")
		testutil.FailErr(t, "get p-sub", err)
		if !ok || rec.Token != tc.wantTok || rec.PendingCommit != nil {
			t.Fatalf("stage %s recovered vault = %+v (ok=%v), want token=%q", tc.stage, rec, ok, tc.wantTok)
		}
	}
}
