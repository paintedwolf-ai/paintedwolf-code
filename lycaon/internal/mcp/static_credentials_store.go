package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"gopkg.in/yaml.v3"
)

const staticCredentialsContext = "mcp static credentials"

// StaticCredentialSnapshot holds the committed credential state prior to an
// in-flight mutation so an interrupted or rolled-back config write can recover
// the previous working credentials.
type StaticCredentialSnapshot struct {
	Token          string            `json:"token,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TokenCleared   bool              `json:"token_cleared,omitempty"`
	HeadersCleared bool              `json:"headers_cleared,omitempty"`
	EnvCleared     bool              `json:"env_cleared,omitempty"`
	Deleted        bool              `json:"deleted,omitempty"`
}

// PendingCredentialCommit binds a staged vault mutation to the target scrubbed
// mcp.yaml digest. If the process crashes before mcp.yaml reaches
// TargetConfigSHA256, startup recovery restores Previous.
type PendingCredentialCommit struct {
	TargetConfigSHA256 string                    `json:"target_config_sha256"`
	Previous           *StaticCredentialSnapshot `json:"previous,omitempty"`
}

// StaticCredentialRecord holds the vault-backed static authentication and
// environment fields for one user-layer MCP provider, together with durable
// migration provenance, field-clear/deletion tombstones, and optional staged
// crash-recovery state.
type StaticCredentialRecord struct {
	Token          string                   `json:"token,omitempty"`
	Headers        map[string]string        `json:"headers,omitempty"`
	Env            map[string]string        `json:"env,omitempty"`
	Migrated       bool                     `json:"migrated,omitempty"`
	TokenCleared   bool                     `json:"token_cleared,omitempty"`
	HeadersCleared bool                     `json:"headers_cleared,omitempty"`
	EnvCleared     bool                     `json:"env_cleared,omitempty"`
	Deleted        bool                     `json:"deleted,omitempty"`
	PendingCommit  *PendingCredentialCommit `json:"pending_commit,omitempty"`
}

// HasSecrets reports whether any non-empty secret value is present.
func (r StaticCredentialRecord) HasSecrets() bool {
	if r.Deleted {
		return false
	}
	return strings.TrimSpace(r.Token) != "" || len(r.Headers) > 0 || len(r.Env) > 0
}

func (r StaticCredentialRecord) snapshot() StaticCredentialSnapshot {
	return StaticCredentialSnapshot{
		Token:          r.Token,
		Headers:        cloneStringMap(r.Headers),
		Env:            cloneStringMap(r.Env),
		TokenCleared:   r.TokenCleared,
		HeadersCleared: r.HeadersCleared,
		EnvCleared:     r.EnvCleared,
		Deleted:        r.Deleted,
	}
}

func snapshotFromRecord(rec StaticCredentialRecord, exists bool) *StaticCredentialSnapshot {
	if !exists {
		return nil
	}
	snap := rec.snapshot()
	return &snap
}

func recordFromSnapshot(snap StaticCredentialSnapshot) StaticCredentialRecord {
	return normalizeRecord(StaticCredentialRecord{
		Token:          snap.Token,
		Headers:        cloneStringMap(snap.Headers),
		Env:            cloneStringMap(snap.Env),
		Migrated:       true,
		TokenCleared:   snap.TokenCleared,
		HeadersCleared: snap.HeadersCleared,
		EnvCleared:     snap.EnvCleared,
		Deleted:        snap.Deleted,
	})
}

func normalizeRecord(rec StaticCredentialRecord) StaticCredentialRecord {
	var pending *PendingCredentialCommit
	if rec.PendingCommit != nil {
		cp := &PendingCredentialCommit{
			TargetConfigSHA256: strings.TrimSpace(rec.PendingCommit.TargetConfigSHA256),
		}
		if rec.PendingCommit.Previous != nil {
			normPrev := normalizeRecord(recordFromSnapshot(*rec.PendingCommit.Previous)).snapshot()
			cp.Previous = &normPrev
		}
		pending = cp
	}
	if rec.Deleted {
		return StaticCredentialRecord{
			Migrated:      true,
			Deleted:       true,
			PendingCommit: pending,
		}
	}
	out := StaticCredentialRecord{
		Token:          strings.TrimSpace(rec.Token),
		Migrated:       rec.Migrated,
		TokenCleared:   rec.TokenCleared,
		HeadersCleared: rec.HeadersCleared,
		EnvCleared:     rec.EnvCleared,
		PendingCommit:  pending,
	}
	if out.Token != "" {
		out.TokenCleared = false
	}
	if len(rec.Headers) > 0 {
		out.Headers = cloneStringMap(rec.Headers)
		out.HeadersCleared = false
	}
	if len(rec.Env) > 0 {
		out.Env = cloneStringMap(rec.Env)
		out.EnvCleared = false
	}
	return out
}

func validateCredentialSnapshot(id string, snap StaticCredentialSnapshot) error {
	if snap.Deleted {
		if snap.Token != "" || len(snap.Headers) > 0 || len(snap.Env) > 0 ||
			snap.TokenCleared || snap.HeadersCleared || snap.EnvCleared {
			return fmt.Errorf("%w: mcp credential record for %q has conflicting deleted state", credentialstore.ErrVaultCorrupt, id)
		}
		return nil
	}
	if snap.Token != "" {
		if strings.TrimSpace(snap.Token) == "" {
			return fmt.Errorf("%w: mcp credential record for %q has whitespace-only token", credentialstore.ErrVaultCorrupt, id)
		}
		if snap.TokenCleared {
			return fmt.Errorf("%w: mcp credential record for %q has both token and token_cleared", credentialstore.ErrVaultCorrupt, id)
		}
	}
	if len(snap.Headers) > 0 && snap.HeadersCleared {
		return fmt.Errorf("%w: mcp credential record for %q has both headers and headers_cleared", credentialstore.ErrVaultCorrupt, id)
	}
	if len(snap.Env) > 0 && snap.EnvCleared {
		return fmt.Errorf("%w: mcp credential record for %q has both env and env_cleared", credentialstore.ErrVaultCorrupt, id)
	}
	return nil
}

func validatePersistedRecord(id string, rec StaticCredentialRecord) error {
	if !rec.Migrated {
		return fmt.Errorf("%w: mcp credential record for %q is missing migrated provenance", credentialstore.ErrVaultCorrupt, id)
	}
	if err := validateCredentialSnapshot(id, rec.snapshot()); err != nil {
		return err
	}
	if rec.PendingCommit != nil {
		if strings.TrimSpace(rec.PendingCommit.TargetConfigSHA256) == "" {
			return fmt.Errorf("%w: mcp credential record for %q has empty pending_commit target digest", credentialstore.ErrVaultCorrupt, id)
		}
		if rec.PendingCommit.Previous != nil {
			if err := validateCredentialSnapshot(id, *rec.PendingCommit.Previous); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodePersistedRecord(id, raw string) (StaticCredentialRecord, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec StaticCredentialRecord
	if err := dec.Decode(&rec); err != nil {
		return StaticCredentialRecord{}, fmt.Errorf("%w: decode mcp credential record for %q: %w", credentialstore.ErrVaultCorrupt, id, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return StaticCredentialRecord{}, fmt.Errorf("%w: trailing data in mcp credential record for %q", credentialstore.ErrVaultCorrupt, id)
	}
	if err := validatePersistedRecord(id, rec); err != nil {
		return StaticCredentialRecord{}, err
	}
	return rec, nil
}

// StaticCredentialStore persists MCP static credentials (token, headers, env)
// inside the encrypted credential vault under NamespaceMCPCredentials.
type StaticCredentialStore struct {
	slot          credentialstore.Slot
	store         *credentialstore.Store
	verifyDurable func(credentialstore.Slot, string, string) error
}

// DefaultCredentialsSlot returns the default vault slot for MCP static credentials.
func DefaultCredentialsSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return staticCredentialsSlot(dir), nil
}

func staticCredentialsSlot(dir string) credentialstore.Slot {
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(dir),
		Namespace: credentialstore.NamespaceMCPCredentials,
		Context:   staticCredentialsContext,
	}
}

// NewStaticCredentialStoreForDir opens the MCP static credential vault namespace in dir.
func NewStaticCredentialStoreForDir(dir string) (*StaticCredentialStore, error) {
	slot := staticCredentialsSlot(dir)
	inner, err := credentialstore.Open(slot, nil)
	if err != nil {
		return nil, err
	}
	return &StaticCredentialStore{
		slot:          slot,
		store:         inner,
		verifyDurable: credentialstore.VerifyDurable,
	}, nil
}

// IDs returns all stored provider keys in the vault namespace.
func (s *StaticCredentialStore) IDs() []string {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.IDs()
}

// Get returns the stored record for providerID, whether a record exists in
// the vault, and an explicit error if the stored payload is corrupt, has
// unknown fields, or violates record shape invariants.
func (s *StaticCredentialStore) Get(providerID string) (StaticCredentialRecord, bool, error) {
	if s == nil || s.store == nil {
		return StaticCredentialRecord{}, false, nil
	}
	id := strings.TrimSpace(providerID)
	raw, ok := s.store.Get(id)
	if !ok {
		return StaticCredentialRecord{}, false, nil
	}
	rec, err := decodePersistedRecord(id, raw.Value())
	if err != nil {
		return StaticCredentialRecord{}, false, err
	}
	return rec, true, nil
}

// PutVerified persists rec under providerID and verifies the durable on-disk
// and native-custodian round-trip. If durable verification fails after Set has
// written to the vault, PutVerified attempts to restore the previous vault entry
// and surfaces any restoration error alongside the verification failure.
func (s *StaticCredentialStore) PutVerified(providerID string, rec StaticCredentialRecord) error {
	if s == nil || s.store == nil {
		return errors.New("mcp static credential store is not initialized")
	}
	key := strings.TrimSpace(providerID)
	if key == "" {
		return errors.New("mcp static credential key is required")
	}
	rec.Migrated = true
	rec = normalizeRecord(rec)
	if err := validatePersistedRecord(key, rec); err != nil {
		return err
	}
	encoded, err := json.Marshal(rec) // #nosec G117 -- encrypted in credential-vault.age before disk commit
	if err != nil {
		return fmt.Errorf("encode %s record: %w", staticCredentialsContext, err)
	}
	payload := string(encoded)
	prevRaw, hadPrevRaw := s.store.Get(key)
	if err := s.store.Set(key, payload); err != nil {
		return err
	}
	verifyFn := s.verifyDurable
	if verifyFn == nil {
		verifyFn = credentialstore.VerifyDurable
	}
	if err := verifyFn(s.slot, key, payload); err != nil {
		verifyErr := fmt.Errorf("verify mcp credential write: %w", err)
		var restoreErr error
		if hadPrevRaw {
			if setErr := s.store.Set(key, prevRaw.Value()); setErr != nil {
				restoreErr = setErr
			} else if durErr := credentialstore.VerifyDurable(s.slot, key, prevRaw.Value()); durErr != nil {
				restoreErr = durErr
			}
		} else {
			restoreErr = s.store.Delete(key)
		}
		if restoreErr != nil {
			return errors.Join(verifyErr, fmt.Errorf("restore mcp credential after verification failure: %w", restoreErr))
		}
		return verifyErr
	}
	return nil
}

// RestoreSnapshot restores providerID's vault state to prev (or deletes the
// record when prev is nil).
func (s *StaticCredentialStore) RestoreSnapshot(providerID string, prev *StaticCredentialSnapshot) error {
	if prev == nil {
		return s.RemoveRecord(providerID)
	}
	return s.PutVerified(providerID, recordFromSnapshot(*prev))
}

// RemoveRecord deletes the raw entry for providerID when rolling back an uncommitted create.
func (s *StaticCredentialStore) RemoveRecord(providerID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Delete(strings.TrimSpace(providerID))
}

func userOverlayConfigDigest(cfg UserMCPConfig) (string, error) {
	scrubbed := UserMCPConfig{
		Providers: make([]MCPProviderOverlay, len(cfg.Providers)),
	}
	for i, ov := range cfg.Providers {
		scrubbed.Providers[i] = scrubOverlayCredentials(ov)
	}
	if len(scrubbed.Providers) == 0 {
		return "empty", nil
	}
	data, err := yaml.Marshal(scrubbed)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func extractOverlayCredentials(ov MCPProviderOverlay) StaticCredentialRecord {
	rec := StaticCredentialRecord{Migrated: true}
	if ov.Token != nil {
		tok := strings.TrimSpace(*ov.Token)
		if tok != "" {
			rec.Token = tok
		} else {
			rec.TokenCleared = true
		}
	}
	if ov.Headers != nil {
		if len(*ov.Headers) > 0 {
			rec.Headers = cloneStringMap(*ov.Headers)
		} else {
			rec.HeadersCleared = true
		}
	}
	if ov.Env != nil {
		if len(*ov.Env) > 0 {
			rec.Env = cloneStringMap(*ov.Env)
		} else {
			rec.EnvCleared = true
		}
	}
	return normalizeRecord(rec)
}

func overlayHasInlineCredentials(ov MCPProviderOverlay) bool {
	return ov.Token != nil || ov.Headers != nil || ov.Env != nil
}

func scrubOverlayCredentials(ov MCPProviderOverlay) MCPProviderOverlay {
	ov.Token = nil
	ov.Headers = nil
	ov.Env = nil
	return ov
}

func hydrateOverlayFromRecord(ov *MCPProviderOverlay, rec StaticCredentialRecord) {
	if ov == nil {
		return
	}
	ov.Token = nil
	ov.Headers = nil
	ov.Env = nil
	if rec.Deleted {
		return
	}
	if rec.Token != "" {
		tok := rec.Token
		ov.Token = &tok
	} else if rec.TokenCleared {
		empty := ""
		ov.Token = &empty
	}
	if len(rec.Headers) > 0 {
		hdrs := cloneStringMap(rec.Headers)
		ov.Headers = &hdrs
	} else if rec.HeadersCleared {
		empty := map[string]string{}
		ov.Headers = &empty
	}
	if len(rec.Env) > 0 {
		env := cloneStringMap(rec.Env)
		ov.Env = &env
	} else if rec.EnvCleared {
		empty := map[string]string{}
		ov.Env = &empty
	}
}

// migrateAndHydrateUserOverlay performs Vault-First, Scrub-Second migration of
// any inline credentials in user mcp.yaml, reconciles any interrupted staged
// commits, and hydrates stored credentials into the returned in-memory
// UserMCPConfig before MergeMCPCatalog runs.
//
// If a vault record is corrupt or has an invalid/conflicting shape, an error
// is returned immediately and both the vault and mcp.yaml are preserved.
func migrateAndHydrateUserOverlay(statePath, globalPath string, user *UserMCPConfig) (*UserMCPConfig, error) {
	if user == nil {
		return &UserMCPConfig{}, nil
	}
	needsMigration := false
	for _, ov := range user.Providers {
		if strings.TrimSpace(ov.ID) != "" && overlayHasInlineCredentials(ov) {
			needsMigration = true
			break
		}
	}

	vaultPath := credentialstore.DefaultVaultPath(statePath)
	_, statErr := os.Stat(vaultPath)
	vaultExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat mcp credential vault: %w", statErr)
	}

	if !needsMigration && !vaultExists {
		return user, nil
	}

	store, err := NewStaticCredentialStoreForDir(statePath)
	if err != nil {
		return nil, fmt.Errorf("open mcp credential vault: %w", err)
	}

	currentDigest, err := userOverlayConfigDigest(*user)
	if err != nil {
		return nil, fmt.Errorf("digest mcp user overlay: %w", err)
	}

	// Validate every stored record in the namespace upfront and reconcile any
	// interrupted PendingCommit records before touching mcp.yaml.
	for _, id := range store.IDs() {
		rec, exists, getErr := store.Get(id)
		if getErr != nil {
			return nil, getErr
		}
		if !exists || rec.PendingCommit == nil {
			continue
		}
		if rec.PendingCommit.TargetConfigSHA256 == currentDigest {
			rec.PendingCommit = nil
			if err := store.PutVerified(id, rec); err != nil {
				return nil, fmt.Errorf("finalize mcp credential commit for %q: %w", id, err)
			}
		} else {
			if err := store.RestoreSnapshot(id, rec.PendingCommit.Previous); err != nil {
				return nil, fmt.Errorf("recover uncommitted mcp credential for %q: %w", id, err)
			}
		}
	}

	if needsMigration {
		scrubbed := UserMCPConfig{
			Providers: make([]MCPProviderOverlay, len(user.Providers)),
		}
		for i, ov := range user.Providers {
			id := strings.TrimSpace(ov.ID)
			if id != "" && overlayHasInlineCredentials(ov) {
				_, hasExisting, getErr := store.Get(id)
				if getErr != nil {
					return nil, getErr
				}
				if !hasExisting {
					inlineRec := extractOverlayCredentials(ov)
					if err := store.PutVerified(id, inlineRec); err != nil {
						return nil, fmt.Errorf("migrate mcp credentials for %q to vault: %w", id, err)
					}
				}
				scrubbed.Providers[i] = scrubOverlayCredentials(ov)
			} else {
				scrubbed.Providers[i] = ov
			}
		}
		if err := saveUserMCPConfig(globalPath, scrubbed); err != nil {
			return nil, fmt.Errorf("scrub migrated mcp.yaml credentials: %w", err)
		}
		user = &scrubbed
	}

	hydrated := UserMCPConfig{
		Providers: make([]MCPProviderOverlay, len(user.Providers)),
	}
	for i, ov := range user.Providers {
		hydratedOv := scrubOverlayCredentials(ov)
		id := strings.TrimSpace(ov.ID)
		if id != "" {
			rec, ok, getErr := store.Get(id)
			if getErr != nil {
				return nil, getErr
			}
			if ok {
				hydrateOverlayFromRecord(&hydratedOv, rec)
			}
		}
		hydrated.Providers[i] = hydratedOv
	}
	return &hydrated, nil
}
