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

	failAllVerify := func(dir string) (*StaticCredentialStore, error) {
		s, openErr := NewStaticCredentialStoreForDir(dir)
		if openErr != nil {
			return nil, openErr
		}
		s.verifyDurable = func(credentialstore.Slot, string, string) error {
			return errors.New("injected durable verification failure")
		}
		return s, nil
	}
	reg.Administration.openStaticCredentials = failAllVerify
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

	reg.Administration.openStaticCredentials = failAllVerify
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

	reg.Administration.openStaticCredentials = failAllVerify
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

// pendingCommitRecoveryFixture drives the sequential crash and recovery scenarios
// of TestPendingCommitRecoveryAcrossCrashValidationAndRollbackBoundaries against one
// shared state directory; each scenario restarts the runtime from disk.
type pendingCommitRecoveryFixture struct {
	t          *testing.T
	ctx        context.Context
	stateDir   string
	globalPath string
}

const (
	pendingCommitProvider        = "p-survive"
	pendingCommitBaseURL         = "https://mcp.example.com/survive"
	pendingCommitTokenV1         = "committed-token-v1"
	pendingCommitURLV4           = "https://mcp.example.com/survive-v4"
	pendingCommitTokenV4         = "committed-token-v4"
	pendingCommitTokenV5CredOnly = "committed-token-v5-cred-only"
)

func newPendingCommitRecoveryFixture(t *testing.T) *pendingCommitRecoveryFixture {
	stateDir := t.TempDir()
	return &pendingCommitRecoveryFixture{
		t:          t,
		ctx:        context.Background(),
		stateDir:   stateDir,
		globalPath: filepath.Join(stateDir, "mcp.yaml"),
	}
}

func (f *pendingCommitRecoveryFixture) newReg() *Runtime {
	r, err := NewRuntime(RuntimeOptions{
		StatePath:          f.stateDir,
		GlobalOverridePath: f.globalPath,
		Connector:          &MockConnector{},
	})
	testutil.FailErr(f.t, "NewRuntime", err)
	testutil.FailErr(f.t, "Load", r.Catalog.Load(f.ctx))
	return r
}

func (f *pendingCommitRecoveryFixture) assertProviderAndToken(id, wantURL, wantToken string) {
	f.t.Helper()
	s, err := NewStaticCredentialStoreForDir(f.stateDir)
	testutil.FailErr(f.t, "open store", err)
	rec, ok, err := s.Get(id)
	testutil.FailErr(f.t, "get "+id, err)
	if wantToken == "" {
		if ok && !rec.Deleted {
			f.t.Fatalf("expected %s to be absent or deleted, got %+v", id, rec)
		}
		return
	}
	if !ok || rec.Token != wantToken || rec.PendingCommit != nil {
		f.t.Fatalf("%s vault record = %+v (ok=%v), want token=%q with nil PendingCommit", id, rec, ok, wantToken)
	}
	cfg, err := LoadUserMCPConfig(f.globalPath)
	testutil.FailErr(f.t, "load mcp.yaml", err)
	ov, found := findProviderOverlayRow(cfg, id)
	if !found || ov.URL == nil || *ov.URL != wantURL {
		f.t.Fatalf("%s mcp.yaml overlay = %+v (found=%v), want URL=%q", id, ov, found, wantURL)
	}
}

func (f *pendingCommitRecoveryFixture) assertInProcessCatalog(r *Runtime, id, wantURL, wantToken string) {
	f.t.Helper()
	row, ok := r.Catalog.GetProvider(f.ctx, CallScope{}, id)
	if !ok || row.URL != wantURL || !row.TokenPresent {
		f.t.Fatalf("in-process GetProvider(%s) = %+v (ok=%v), want URL=%q TokenPresent=true", id, row, ok, wantURL)
	}
	ov, found := findProviderOverlayRow(r.Catalog.user, id)
	if !found || ov.Token == nil || *ov.Token != wantToken {
		f.t.Fatalf("in-process hydrated token for %s = %v (found=%v), want %q", id, ov.Token, found, wantToken)
	}
}

// faultyStaticStore replaces openStaticCredentials with a store whose Nth setEntry
// and verifyDurable calls fail, simulating partial disk failures around commit marks.
func faultyStaticStore(failSetCall, failVerifyCall int) func(string) (*StaticCredentialStore, error) {
	return func(dir string) (*StaticCredentialStore, error) {
		s, openErr := NewStaticCredentialStoreForDir(dir)
		if openErr != nil {
			return nil, openErr
		}
		setCalls, verifyCalls := 0, 0
		s.setEntry = func(key, val string) error {
			setCalls++
			if setCalls == failSetCall {
				return errors.New("injected restoreRaw set failure")
			}
			return s.store.Set(key, val)
		}
		s.verifyDurable = func(slot credentialstore.Slot, key, want string) error {
			verifyCalls++
			if verifyCalls == failVerifyCall {
				return errors.New("injected durable verification failure")
			}
			return credentialstore.VerifyDurable(slot, key, want)
		}
		return s, nil
	}
}

func TestPendingCommitRecoveryAcrossCrashValidationAndRollbackBoundaries(t *testing.T) {
	f := newPendingCommitRecoveryFixture(t)
	f.createSurvivorProvider()
	f.crashCredentialOnlyUpdateAfterPendingWrite()
	f.crashAfterConfigWriteLeavesUncommittedOverlay()
	f.recoveryFaultBeforeConfigWriteKeepsPendingCommit()
	f.recoveryFaultBeforeVaultRestoreKeepsPendingCommit()
	f.restartCompletesVaultRestoration()
	f.crashBeforeFileRollbackRejectsUnknownRecipe()
	f.crashAfterFileRollbackRejectsInvalidUpdate()
	f.crashAfterCommitMarkAndFinalizeRecovers()
	f.deferredFinalizationSurvivesRestart()
	f.unverifiedCommitMarkRollsBackToPreviousToken()
	f.equalDigestCommitMarkRollsForwardCredentialOnly()
	f.crashDuringDeleteKeepsProvider()
}

func (f *pendingCommitRecoveryFixture) createSurvivorProvider() {
	reg := f.newReg()
	enabled := false
	_, err := reg.Administration.CreateProvider(f.ctx, CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      pendingCommitProvider,
		URL:     pendingCommitBaseURL,
		Enabled: &enabled,
		Token:   pendingCommitTokenV1,
	}, "")
	testutil.FailErr(f.t, "CreateProvider p-survive", err)
}

// 1. Credential-only UpdateProvider crashing at "after_pending_write".
func (f *pendingCommitRecoveryFixture) crashCredentialOnlyUpdateAfterPendingWrite() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_pending_write" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after pending write")
		}
		return nil
	}
	credOnlyTok := "uncommitted-credential-only-v2"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		Token: &credOnlyTok,
	}, ""); err == nil {
		f.t.Fatal("expected simulated crash on credential-only update")
	}
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitBaseURL, pendingCommitTokenV1)
}

// 2. Combined URL + token UpdateProvider crashing at "after_config_write",
// followed by recovery faults before and after writeOrRemoveUserMCPConfig.
func (f *pendingCommitRecoveryFixture) crashAfterConfigWriteLeavesUncommittedOverlay() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_config_write" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after config write")
		}
		return nil
	}
	uncommittedURL := "https://mcp.example.com/uncommitted-new-host"
	uncommittedTok := "uncommitted-new-token"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		URL:   &uncommittedURL,
		Token: &uncommittedTok,
	}, ""); err == nil {
		f.t.Fatal("expected simulated crash at after_config_write")
	}
	midCrashYAML, err := os.ReadFile(f.globalPath)
	testutil.FailErr(f.t, "read mid-crash mcp.yaml", err)
	if !strings.Contains(string(midCrashYAML), uncommittedURL) {
		f.t.Fatalf("expected after_config_write crash to leave %q on disk:\n%s", uncommittedURL, string(midCrashYAML))
	}
}

// 2a. Recovery fails at "before_recovery_config_write": PendingCommit stays intact.
func (f *pendingCommitRecoveryFixture) recoveryFaultBeforeConfigWriteKeepsPendingCommit() {
	rFail1, err := NewRuntime(RuntimeOptions{StatePath: f.stateDir, GlobalOverridePath: f.globalPath, Connector: &MockConnector{}})
	testutil.FailErr(f.t, "NewRuntime rFail1", err)
	rFail1.Catalog.recoveryHook = func(stage, _ string) error {
		if stage == "before_recovery_config_write" {
			return errors.New("simulated disk error restoring mcp.yaml during recovery")
		}
		return nil
	}
	if err := rFail1.Catalog.Load(f.ctx); err == nil {
		f.t.Fatal("expected Catalog.Load to fail when before_recovery_config_write fails")
	}
}

// 2b. Recovery writes restored mcp.yaml then crashes before RestoreSnapshot.
func (f *pendingCommitRecoveryFixture) recoveryFaultBeforeVaultRestoreKeepsPendingCommit() {
	rFail2, err := NewRuntime(RuntimeOptions{StatePath: f.stateDir, GlobalOverridePath: f.globalPath, Connector: &MockConnector{}})
	testutil.FailErr(f.t, "NewRuntime rFail2", err)
	rFail2.Catalog.recoveryHook = func(stage, _ string) error {
		if stage == "after_recovery_config_write_before_vault_restore" {
			return errors.New("simulated crash after recovery config write before vault restore")
		}
		return nil
	}
	if err := rFail2.Catalog.Load(f.ctx); err == nil {
		f.t.Fatal("expected Catalog.Load to fail at after_recovery_config_write_before_vault_restore")
	}
}

// 2c. Subsequent restart completes vault restoration coherently.
func (f *pendingCommitRecoveryFixture) restartCompletesVaultRestoration() {
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitBaseURL, pendingCommitTokenV1)
}

// 3. Invalid create and invalid update crashing during rollback boundaries.
func (f *pendingCommitRecoveryFixture) crashBeforeFileRollbackRejectsUnknownRecipe() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, _, _ string) error {
		if stage == "before_file_rollback" {
			return errors.New("simulated crash before file rollback")
		}
		return nil
	}
	badRecipe := "nonexistent-recipe"
	badTok := "orphan-rejected-create-token"
	if _, err := reg.Administration.persistNewOverlay(f.ctx, CallScope{}, "rejected-create", MCPProviderOverlay{
		ID:     "rejected-create",
		Recipe: &badRecipe,
		Token:  &badTok,
	}, ""); err == nil {
		f.t.Fatal("expected persistNewOverlay with unknown recipe to fail Validate")
	}
	rawYAML, err := os.ReadFile(f.globalPath)
	testutil.FailErr(f.t, "read mid-rollback mcp.yaml", err)
	if !strings.Contains(string(rawYAML), "rejected-create") {
		f.t.Fatalf("expected crash before file rollback to leave rejected-create in mcp.yaml:\n%s", string(rawYAML))
	}
	f.newReg()
	f.assertProviderAndToken("rejected-create", "", "")
	rawYAMLAfter, err := os.ReadFile(f.globalPath)
	testutil.FailErr(f.t, "read recovered mcp.yaml", err)
	if strings.Contains(string(rawYAMLAfter), "rejected-create") {
		f.t.Fatalf("expected startup recovery to remove uncommitted rejected-create from mcp.yaml:\n%s", string(rawYAMLAfter))
	}
}

func (f *pendingCommitRecoveryFixture) crashAfterFileRollbackRejectsInvalidUpdate() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_file_rollback_before_cred_rollback" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after file rollback before cred rollback")
		}
		return nil
	}
	emptyURL := ""
	rejectedUpdateTok := "orphan-rejected-update-token"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		URL:   &emptyURL,
		Token: &rejectedUpdateTok,
	}, ""); err == nil {
		f.t.Fatal("expected UpdateProvider clearing transport to fail Validate")
	}
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitBaseURL, pendingCommitTokenV1)
}

// 4. Crash after Committed=true mark ("after_commit_mark") and "after_finalize".
func (f *pendingCommitRecoveryFixture) crashAfterCommitMarkAndFinalizeRecovers() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_commit_mark" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after commit mark")
		}
		return nil
	}
	urlV2 := "https://mcp.example.com/survive-v2"
	tokV2 := "committed-token-v2"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		URL:   &urlV2,
		Token: &tokV2,
	}, ""); err == nil {
		f.t.Fatal("expected simulated crash at after_commit_mark")
	}
	reg = f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, urlV2, tokV2)

	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_finalize" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after finalize")
		}
		return nil
	}
	tokV3 := "committed-token-v3"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{Token: &tokV3}, ""); err == nil {
		f.t.Fatal("expected simulated crash at after_finalize")
	}
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, urlV2, tokV3)
}

// 5. Finalization verification failure AFTER Committed=true is durable:
// UpdateProvider reports success with finalization deferred to startup;
// UpdateFile does not roll back mcp.yaml, and startup finalizes PendingCommit=nil.
func (f *pendingCommitRecoveryFixture) deferredFinalizationSurvivesRestart() {
	reg := f.newReg()
	reg.Administration.openStaticCredentials = faultyStaticStore(0, 3)
	urlV4 := pendingCommitURLV4
	tokV4 := pendingCommitTokenV4
	updatedRow, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		URL:   &urlV4,
		Token: &tokV4,
	}, "")
	testutil.FailErr(f.t, "UpdateProvider with deferred finalization", err)
	if updatedRow.URL != urlV4 || !updatedRow.TokenPresent {
		f.t.Fatalf("UpdateProvider row = %+v, want URL=%q TokenPresent=true", updatedRow, urlV4)
	}
	f.assertInProcessCatalog(reg, pendingCommitProvider, urlV4, tokV4)
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, urlV4, tokV4)
}

// 6a. Commit-marker verification failure + partial recovery failure on URL+token update
// (TargetConfigSHA256 != PreviousConfigSHA256; restoreRaw fails and rollback crashes at
// after_file_rollback_before_cred_rollback after restoring old mcp.yaml): both in-process
// catalog and startup reconciliation match PreviousConfigSHA256 and preserve V4.
func (f *pendingCommitRecoveryFixture) unverifiedCommitMarkRollsBackToPreviousToken() {
	reg := f.newReg()
	reg.Administration.openStaticCredentials = faultyStaticStore(3, 2)
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_file_rollback_before_cred_rollback" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after file rollback with unverified commit mark")
		}
		return nil
	}
	urlUnverified := "https://mcp.example.com/unverified-commit-mark"
	tokUnverified := "unverified-commit-mark-token"
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		URL:   &urlUnverified,
		Token: &tokUnverified,
	}, ""); err == nil {
		f.t.Fatal("expected UpdateProvider to fail when commit-mark verification and rollback fail")
	}
	f.assertInProcessCatalog(reg, pendingCommitProvider, pendingCommitURLV4, pendingCommitTokenV4)
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitURLV4, pendingCommitTokenV4)
}

// 6b. Equal-digest path (credential-only update, TargetConfigSHA256 == PreviousConfigSHA256)
// with the case-6 shape (commit-mark verify fails, restoreRaw fails, crash before credential
// rollback): Committed=true remains in the vault at TargetConfigSHA256, so both in-process
// catalog and post-restart reconciliation coherently roll forward to tokV5CredOnly.
func (f *pendingCommitRecoveryFixture) equalDigestCommitMarkRollsForwardCredentialOnly() {
	reg := f.newReg()
	reg.Administration.openStaticCredentials = faultyStaticStore(3, 2)
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_file_rollback_before_cred_rollback" && op == "update" && id == pendingCommitProvider {
			return errors.New("simulated crash after file rollback on equal-digest commit mark")
		}
		return nil
	}
	tokV5CredOnly := pendingCommitTokenV5CredOnly
	if _, err := reg.Administration.UpdateProvider(f.ctx, CallScope{}, pendingCommitProvider, api.UpdateMcpProviderRequest{
		Token: &tokV5CredOnly,
	}, ""); err == nil {
		f.t.Fatal("expected credential-only UpdateProvider to fail when commit-mark verify and rollback crash")
	}
	f.assertInProcessCatalog(reg, pendingCommitProvider, pendingCommitURLV4, tokV5CredOnly)
	reg = f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitURLV4, tokV5CredOnly)
	f.assertInProcessCatalog(reg, pendingCommitProvider, pendingCommitURLV4, tokV5CredOnly)
}

func (f *pendingCommitRecoveryFixture) crashDuringDeleteKeepsProvider() {
	reg := f.newReg()
	reg.Administration.crashHook = func(stage, op, id string) error {
		if stage == "after_config_write" && op == "delete" && id == pendingCommitProvider {
			return errors.New("simulated crash during delete after config write")
		}
		return nil
	}
	if err := reg.Administration.DeleteOverlay(f.ctx, pendingCommitProvider, ""); err == nil {
		f.t.Fatal("expected simulated crash on delete")
	}
	f.newReg()
	f.assertProviderAndToken(pendingCommitProvider, pendingCommitURLV4, pendingCommitTokenV5CredOnly)
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
		if stage == "finalize_verify_fail_exit" ||
			stage == "commit_mark_verify_and_restore_fail_exit" ||
			stage == "commit_mark_verify_and_restore_fail_cred_only_exit" {
			failSet, failVerify := 0, 3
			if stage != "finalize_verify_fail_exit" {
				failSet, failVerify = 3, 2
				r.Administration.crashHook = func(hookStage, _, _ string) error {
					if hookStage == "after_file_rollback_before_cred_rollback" {
						os.Exit(42)
					}
					return nil
				}
			}
			r.Administration.openStaticCredentials = func(dir string) (*StaticCredentialStore, error) {
				s, openErr := NewStaticCredentialStoreForDir(dir)
				if openErr != nil {
					return nil, openErr
				}
				setCalls, verifyCalls := 0, 0
				s.setEntry = func(key, val string) error {
					setCalls++
					if setCalls == failSet {
						return errors.New("subprocess restoreRaw failure")
					}
					return s.store.Set(key, val)
				}
				s.verifyDurable = func(slot credentialstore.Slot, key, want string) error {
					verifyCalls++
					if verifyCalls == failVerify {
						return errors.New("subprocess verify failure")
					}
					return credentialstore.VerifyDurable(slot, key, want)
				}
				return s, nil
			}
			nextURL := "https://mcp.example.com/sub-next"
			nextTok := "subprocess-next-tok"
			req := api.UpdateMcpProviderRequest{URL: &nextURL, Token: &nextTok}
			if stage == "commit_mark_verify_and_restore_fail_cred_only_exit" {
				req = api.UpdateMcpProviderRequest{Token: &nextTok}
			}
			_, _ = r.Administration.UpdateProvider(ctx, CallScope{}, "p-sub", req, "")
			os.Exit(42)
		}
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
		{stage: "finalize_verify_fail_exit", wantURL: "https://mcp.example.com/sub-next", wantTok: "subprocess-next-tok"},
		{stage: "commit_mark_verify_and_restore_fail_exit", wantURL: "https://mcp.example.com/sub-init", wantTok: "subprocess-init-tok"},
		{stage: "commit_mark_verify_and_restore_fail_cred_only_exit", wantURL: "https://mcp.example.com/sub-init", wantTok: "subprocess-next-tok"},
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
