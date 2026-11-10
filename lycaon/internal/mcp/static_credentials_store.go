package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const staticCredentialsContext = "mcp static credentials"

// StaticCredentialRecord holds the vault-backed static authentication and
// environment fields for one user-layer MCP provider, together with durable
// migration provenance and field-clear/deletion tombstones so stale inline YAML
// from an interrupted scrub can never overwrite a newer clear or delete.
type StaticCredentialRecord struct {
	Token          string            `json:"token,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Migrated       bool              `json:"migrated,omitempty"`
	TokenCleared   bool              `json:"token_cleared,omitempty"`
	HeadersCleared bool              `json:"headers_cleared,omitempty"`
	EnvCleared     bool              `json:"env_cleared,omitempty"`
	Deleted        bool              `json:"deleted,omitempty"`
}

// HasSecrets reports whether any non-empty secret value is present.
func (r StaticCredentialRecord) HasSecrets() bool {
	if r.Deleted {
		return false
	}
	return strings.TrimSpace(r.Token) != "" || len(r.Headers) > 0 || len(r.Env) > 0
}

func normalizeRecord(rec StaticCredentialRecord) StaticCredentialRecord {
	if rec.Deleted {
		return StaticCredentialRecord{
			Migrated: true,
			Deleted:  true,
		}
	}
	out := StaticCredentialRecord{
		Token:          strings.TrimSpace(rec.Token),
		Migrated:       rec.Migrated,
		TokenCleared:   rec.TokenCleared,
		HeadersCleared: rec.HeadersCleared,
		EnvCleared:     rec.EnvCleared,
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

// StaticCredentialStore persists MCP static credentials (token, headers, env)
// inside the encrypted credential vault under NamespaceMCPCredentials.
type StaticCredentialStore struct {
	slot  credentialstore.Slot
	store *credentialstore.Store
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
	return &StaticCredentialStore{slot: slot, store: inner}, nil
}

// Get returns the stored record for providerID and whether a record exists in
// the vault (including cleared or deleted tombstones).
func (s *StaticCredentialStore) Get(providerID string) (StaticCredentialRecord, bool) {
	if s == nil || s.store == nil {
		return StaticCredentialRecord{}, false
	}
	raw, ok := s.store.Get(strings.TrimSpace(providerID))
	if !ok {
		return StaticCredentialRecord{}, false
	}
	var rec StaticCredentialRecord
	if err := json.Unmarshal([]byte(raw.Value()), &rec); err != nil {
		return StaticCredentialRecord{}, false
	}
	rec = normalizeRecord(rec)
	if !rec.Migrated && !rec.HasSecrets() && !rec.TokenCleared && !rec.HeadersCleared && !rec.EnvCleared && !rec.Deleted {
		return StaticCredentialRecord{}, false
	}
	return rec, true
}

// PutVerified persists rec under providerID and verifies the durable on-disk
// and native-custodian round-trip via credentialstore.VerifyDurable.
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
	encoded, err := json.Marshal(rec) // #nosec G117 -- encrypted in credential-vault.age before disk commit
	if err != nil {
		return fmt.Errorf("encode %s record: %w", staticCredentialsContext, err)
	}
	payload := string(encoded)
	if err := s.store.Set(key, payload); err != nil {
		return err
	}
	if err := credentialstore.VerifyDurable(s.slot, key, payload); err != nil {
		return fmt.Errorf("verify mcp credential write: %w", err)
	}
	return nil
}

// RemoveRecord deletes the raw entry for providerID when rolling back an uncommitted create.
func (s *StaticCredentialStore) RemoveRecord(providerID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Delete(strings.TrimSpace(providerID))
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
// any inline credentials in user mcp.yaml and hydrates stored credentials into
// the returned in-memory UserMCPConfig before MergeMCPCatalog runs.
//
// If a vault record (or clear/delete tombstone) already exists for a provider,
// stale inline YAML fields from an interrupted scrub are never allowed to
// overwrite or backfill the vault state.
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

	if needsMigration {
		scrubbed := UserMCPConfig{
			Providers: make([]MCPProviderOverlay, len(user.Providers)),
		}
		for i, ov := range user.Providers {
			id := strings.TrimSpace(ov.ID)
			if id != "" && overlayHasInlineCredentials(ov) {
				if _, hasExisting := store.Get(id); !hasExisting {
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
			if rec, ok := store.Get(id); ok {
				hydrateOverlayFromRecord(&hydratedOv, rec)
			}
		}
		hydrated.Providers[i] = hydratedOv
	}
	return &hydrated, nil
}
