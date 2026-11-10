package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const staticCredentialsContext = "mcp static credentials"

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

// migrateAndHydrateUserOverlay performs a read-only preflight validation pass
// over all vault records, reconciles any interrupted staged commits (when
// reconcilePending is true), migrates and scrubs any inline credentials in
// user mcp.yaml (Vault-First, Scrub-Second), and hydrates stored credentials
// into the returned in-memory UserMCPConfig.
func migrateAndHydrateUserOverlay(
	statePath, globalPath string,
	user *UserMCPConfig,
	reconcilePending bool,
	recoveryHook func(stage, providerID string) error,
) (*UserMCPConfig, error) {
	if user == nil {
		return &UserMCPConfig{}, nil
	}
	needsMigration := hasInlineOverlayCredentials(user.Providers)
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

	// Pass 1: Read-only preflight validation over every stored record before
	// performing any recovery write or mcp.yaml mutation.
	ids := store.IDs()
	records := make(map[string]StaticCredentialRecord, len(ids))
	for _, id := range ids {
		rec, exists, getErr := store.Get(id)
		if getErr != nil {
			return nil, getErr
		}
		if !exists {
			continue
		}
		if reconcilePending && rec.PendingCommit != nil && rec.PendingCommit.Committed {
			if rec.PendingCommit.TargetConfigSHA256 != currentDigest {
				return nil, fmt.Errorf("%w: committed mcp credential record for %q does not match mcp.yaml digest", credentialstore.ErrVaultCorrupt, id)
			}
		}
		records[id] = rec
	}

	// Pass 2: Reconcile interrupted PendingCommit records only outside an active
	// in-process overlay transaction. Restore mcp.yaml on disk FIRST while
	// uncommitted PendingCommit records remain durably in the vault, then clear
	// or restore vault records so any interruption during recovery is idempotent.
	if reconcilePending {
		configChanged := false
		for _, id := range ids {
			rec, ok := records[id]
			if !ok || rec.PendingCommit == nil || rec.PendingCommit.Committed {
				continue
			}
			pending := rec.PendingCommit
			curOv, hasCurOv := findProviderOverlayRow(user, id)
			if pending.HadPreviousOverlay && pending.PreviousOverlay != nil {
				wantOv := cloneProviderOverlay(*pending.PreviousOverlay)
				if !hasCurOv || !reflect.DeepEqual(curOv, wantOv) {
					upsertProviderOverlayRow(user, wantOv)
					configChanged = true
				}
			} else if hasCurOv {
				removeProviderOverlayRow(user, id)
				configChanged = true
			}
		}
		if configChanged && strings.TrimSpace(globalPath) != "" {
			if recoveryHook != nil {
				if err := recoveryHook("before_recovery_config_write", ""); err != nil {
					return nil, fmt.Errorf("restore uncommitted mcp.yaml overlay: %w", err)
				}
			}
			if err := writeOrRemoveUserMCPConfig(globalPath, *user); err != nil {
				return nil, fmt.Errorf("restore uncommitted mcp.yaml overlay: %w", err)
			}
			if recoveryHook != nil {
				if err := recoveryHook("after_recovery_config_write_before_vault_restore", ""); err != nil {
					return nil, err
				}
			}
		}
		for _, id := range ids {
			rec, ok := records[id]
			if !ok || rec.PendingCommit == nil {
				continue
			}
			pending := rec.PendingCommit
			if pending.Committed {
				rec.PendingCommit = nil
				if err := store.PutVerified(id, rec); err != nil {
					return nil, fmt.Errorf("finalize mcp credential commit for %q: %w", id, err)
				}
				continue
			}
			if recoveryHook != nil {
				if err := recoveryHook("before_recovery_vault_restore", id); err != nil {
					return nil, err
				}
			}
			if err := store.RestoreSnapshot(id, pending.Previous); err != nil {
				return nil, fmt.Errorf("recover uncommitted mcp credential for %q: %w", id, err)
			}
		}
		needsMigration = hasInlineOverlayCredentials(user.Providers)
	}

	if needsMigration {
		scrubbed := UserMCPConfig{Providers: make([]MCPProviderOverlay, len(user.Providers))}
		for i, ov := range user.Providers {
			id := strings.TrimSpace(ov.ID)
			if id != "" && overlayHasInlineCredentials(ov) {
				_, hasExisting, getErr := store.Get(id)
				if getErr != nil {
					return nil, getErr
				}
				if !hasExisting {
					if err := store.PutVerified(id, extractOverlayCredentials(ov)); err != nil {
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

	hydrated := UserMCPConfig{Providers: make([]MCPProviderOverlay, len(user.Providers))}
	for i, ov := range user.Providers {
		hydratedOv := scrubOverlayCredentials(ov)
		if id := strings.TrimSpace(ov.ID); id != "" {
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

func hasInlineOverlayCredentials(providers []MCPProviderOverlay) bool {
	for _, ov := range providers {
		if strings.TrimSpace(ov.ID) != "" && overlayHasInlineCredentials(ov) {
			return true
		}
	}
	return false
}
