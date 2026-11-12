package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/sony/gobreaker"
)

// ProviderCatalog owns scoped provider resolution and catalog observation.
type ProviderCatalog struct {
	mu             *sync.RWMutex
	Connections    *ConnectionPool
	Credentials    *ProviderCredentials
	Tools          *ToolDiscovery
	globalPath     string
	statePath      string
	distro         *DistroMCPConfig
	user           *UserMCPConfig
	deviceCatalog  []MergedMCPProviderEntry
	deviceRejected []RejectedRow
	overlayApplies func(ctx context.Context, projectDir string) bool
	recoveryHook   func(stage, providerID string) error
}

// ProviderAdministration owns provider overlay transactions.
type ProviderAdministration struct {
	mu                    *sync.RWMutex
	Calls                 *ToolCalls
	Catalog               *ProviderCatalog
	Connections           *ConnectionPool
	Credentials           *ProviderCredentials
	Tools                 *ToolDiscovery
	onSettings            func()
	OpenStaticCredentials func(dir string) (*StaticCredentialStore, error)
	beforeConfigWrite     func(op, providerID string) error
	crashHook             func(stage, op, providerID string) error
}

type credentialOverlayTx struct {
	admin            *ProviderAdministration
	op               string
	providerID       string
	isUserScope      bool
	store            *StaticCredentialStore
	staged           StaticCredentialRecord
	prevSnapshot     *StaticCredentialSnapshot
	prevConfigDigest string
	vaultTouched     bool
	committed        bool
}

func newCredentialOverlayTx(admin *ProviderAdministration, op, providerID, scopeProjectDir string) *credentialOverlayTx {
	return &credentialOverlayTx{
		admin:       admin,
		op:          op,
		providerID:  providerID,
		isUserScope: strings.TrimSpace(scopeProjectDir) == "",
	}
}

func (tx *credentialOverlayTx) captureBefore(cfg *UserMCPConfig) (MCPProviderOverlay, bool, error) {
	prevOv, hadPrevOv := findProviderOverlayRow(cfg, tx.providerID)
	if !tx.isUserScope {
		return prevOv, hadPrevOv, nil
	}
	digest, err := userOverlayConfigDigest(*cfg)
	if err != nil {
		return MCPProviderOverlay{}, false, err
	}
	tx.prevConfigDigest = digest
	return prevOv, hadPrevOv, nil
}

func (tx *credentialOverlayTx) fireCrash(stage string) error {
	if !tx.isUserScope || tx.admin.crashHook == nil {
		return nil
	}
	if err := tx.admin.crashHook(stage, tx.op, tx.providerID); err != nil {
		return fmt.Errorf("%w: %w", catalogruntime.ErrSimulatedCrash, err)
	}
	return nil
}

func (tx *credentialOverlayTx) stage(
	cfg UserMCPConfig,
	prevOv MCPProviderOverlay,
	hadPrevOv bool,
	decide func(prevCreds StaticCredentialRecord, hadPrev bool) (StaticCredentialRecord, bool),
) error {
	if !tx.isUserScope {
		return nil
	}
	var err error
	tx.store, err = tx.admin.openCredentialStore()
	if err != nil {
		return err
	}
	prevCreds, hadPrev, err := tx.store.Get(tx.providerID)
	if err != nil {
		return err
	}
	tx.prevSnapshot = snapshotFromRecord(prevCreds, hadPrev)
	nextCreds, touch := decide(prevCreds, hadPrev)
	if touch {
		targetDigest, err := userOverlayConfigDigest(cfg)
		if err != nil {
			return err
		}
		var prevOvPtr *MCPProviderOverlay
		if hadPrevOv {
			scrubbed := scrubOverlayCredentials(cloneProviderOverlay(prevOv))
			prevOvPtr = &scrubbed
		}
		nextCreds.PendingCommit = &PendingCredentialCommit{
			TargetConfigSHA256:   targetDigest,
			PreviousConfigSHA256: tx.prevConfigDigest,
			Committed:            false,
			Previous:             tx.prevSnapshot,
			HadPreviousOverlay:   hadPrevOv,
			PreviousOverlay:      prevOvPtr,
		}
		tx.staged = nextCreds
		tx.vaultTouched = true
		if err := tx.store.PutVerified(tx.providerID, nextCreds); err != nil {
			return err
		}
		if err := tx.fireCrash("after_pending_write"); err != nil {
			return err
		}
	}
	if tx.admin.beforeConfigWrite != nil {
		if err := tx.admin.beforeConfigWrite(tx.op, tx.providerID); err != nil {
			return err
		}
	}
	return nil
}

func (tx *credentialOverlayTx) saveConfig(path string, cfg UserMCPConfig) error {
	if err := saveUserMCPConfig(path, cfg); err != nil {
		return err
	}
	return tx.fireCrash("after_config_write")
}

func (tx *credentialOverlayTx) writeOrRemoveConfig(path string, cfg UserMCPConfig) error {
	if err := writeOrRemoveUserMCPConfig(path, cfg); err != nil {
		return err
	}
	return tx.fireCrash("after_config_write")
}

func (tx *credentialOverlayTx) onValidateReject(err error) error {
	if crashErr := tx.fireCrash("before_file_rollback"); crashErr != nil {
		return errors.Join(err, crashErr)
	}
	return err
}

func (tx *credentialOverlayTx) commit() error {
	if !tx.isUserScope || !tx.vaultTouched || tx.store == nil {
		return nil
	}
	committed := tx.staged
	cp := *tx.staged.PendingCommit
	cp.Committed = true
	committed.PendingCommit = &cp
	if err := tx.store.PutVerified(tx.providerID, committed); err != nil {
		return err
	}
	tx.committed = true
	if err := tx.fireCrash("after_commit_mark"); err != nil {
		return err
	}
	finalized := committed
	finalized.PendingCommit = nil
	if err := tx.store.PutVerified(tx.providerID, finalized); err != nil {
		slog.Warn("mcp credential commit finalization deferred to startup",
			"op", tx.op, "provider_id", tx.providerID, "error", err)
	}
	return tx.fireCrash("after_finalize")
}

func (tx *credentialOverlayTx) isCommitted() bool {
	return tx.committed
}

func (tx *credentialOverlayTx) rollback(context.Context) error {
	if err := tx.fireCrash("after_file_rollback_before_cred_rollback"); err != nil {
		return err
	}
	if !tx.vaultTouched || tx.store == nil {
		return nil
	}
	return tx.store.RestoreSnapshot(tx.providerID, tx.prevSnapshot)
}

// ProviderCredentials owns provider credential and OAuth state.
type ProviderCredentials struct {
	mu             *sync.RWMutex
	Administration *ProviderAdministration
	Catalog        *ProviderCatalog
	Connections    *ConnectionPool
	Tools          *ToolDiscovery
	oauthStore     *OAuthTokenStore
	oauth          *OAuthClient
	recipes        *RecipeCatalog
	authRequired   map[string]bool
}

// ConnectionPool owns provider sessions and subprocess lifetime.
type ConnectionPool struct {
	mu                 *sync.RWMutex
	Credentials        *ProviderCredentials
	Tools              *ToolDiscovery
	sessionMu          sync.Mutex
	sessions           map[sessionRef]*pooledSession
	connector          SessionConnector
	apiToken           string
	deviceProbeRootsFn func() []string
	lifeCtx            context.Context
	lifeCancel         context.CancelFunc
	closed             bool
}

// ToolDiscovery owns tool discovery and atomic publication.
type ToolDiscovery struct {
	mu           *sync.RWMutex
	Calls        *ToolCalls
	Catalog      *ProviderCatalog
	Connections  *ConnectionPool
	syncMu       sync.Mutex
	pins         *toolPins
	syncErrors   map[string]string
	syncOK       map[string]bool
	toolRefs     map[string]registeredTool
	toolDefs     map[string][]sanitizedToolDefinition
	toolRegistry *tools.DefaultRegistry
	resync       chan string
}

// ToolCalls owns screened provider invocation and circuit breakers.
type ToolCalls struct {
	mu            *sync.RWMutex
	Catalog       *ProviderCatalog
	Connections   *ConnectionPool
	Tools         *ToolDiscovery
	breakers      map[string]*gobreaker.CircuitBreaker
	breakerTh     uint32
	secretMatcher *secretmatch.Matcher
	secretAsk     secretmatch.AskFunc
}
