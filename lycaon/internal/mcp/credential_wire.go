package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

// CredentialWire is how a stored static token is placed on the HTTP request.
type CredentialWire string

const (
	CredentialWireBearer     CredentialWire = "bearer"
	CredentialWireTokenToken CredentialWire = "token_token"
	CredentialWireHeader     CredentialWire = "header"
)

// NormalizeCredentialWire maps empty to bearer. ok is false for an unknown value.
func NormalizeCredentialWire(raw string) (CredentialWire, bool) {
	switch CredentialWire(strings.TrimSpace(raw)) {
	case "", CredentialWireBearer:
		return CredentialWireBearer, true
	case CredentialWireTokenToken:
		return CredentialWireTokenToken, true
	case CredentialWireHeader:
		return CredentialWireHeader, true
	default:
		return "", false
	}
}

// formatStaticCredential returns the header name and value for a stored token.
// Unknown wire, or header placement without a name, yields empty strings.
func formatStaticCredential(wire, header, secret string) (name, value string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", ""
	}
	kind, ok := NormalizeCredentialWire(wire)
	if !ok {
		return "", ""
	}
	switch kind {
	case CredentialWireTokenToken:
		return "Authorization", "Token token=" + secret
	case CredentialWireHeader:
		name = strings.TrimSpace(header)
		if !validCredentialHeader(name) {
			return "", ""
		}
		return name, secret
	default:
		return "Authorization", "Bearer " + secret
	}
}

func validCredentialHeader(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !isHTTPTokenRune(r) {
			return false
		}
	}
	return true
}

func isHTTPTokenRune(r rune) bool {
	if r > unicode.MaxASCII {
		return false
	}
	switch r {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func validateCredentialPlacement(wire, header string) string {
	kind, ok := NormalizeCredentialWire(wire)
	if !ok {
		return RejectInvalidEntry
	}
	if kind == CredentialWireHeader && !validCredentialHeader(strings.TrimSpace(header)) {
		return RejectInvalidEntry
	}
	return ""
}

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

// PendingCredentialCommit binds a staged vault mutation to an explicit
// post-validation commit marker and target scrubbed mcp.yaml digest, while
// retaining the prior credential snapshot and prior mcp.yaml overlay row so
// an interrupted or failed transaction can roll back both stores.
type PendingCredentialCommit struct {
	TargetConfigSHA256 string                    `json:"target_config_sha256"`
	Committed          bool                      `json:"committed,omitempty"`
	Previous           *StaticCredentialSnapshot `json:"previous,omitempty"`
	HadPreviousOverlay bool                      `json:"had_previous_overlay,omitempty"`
	PreviousOverlay    *MCPProviderOverlay       `json:"previous_overlay,omitempty"`
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
			Committed:          rec.PendingCommit.Committed,
			HadPreviousOverlay: rec.PendingCommit.HadPreviousOverlay,
		}
		if rec.PendingCommit.Previous != nil {
			normPrev := normalizeRecord(recordFromSnapshot(*rec.PendingCommit.Previous)).snapshot()
			cp.Previous = &normPrev
		}
		if rec.PendingCommit.PreviousOverlay != nil {
			scrubbedOv := scrubOverlayCredentials(cloneProviderOverlay(*rec.PendingCommit.PreviousOverlay))
			cp.PreviousOverlay = &scrubbedOv
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
		if !rec.PendingCommit.HadPreviousOverlay && rec.PendingCommit.PreviousOverlay != nil {
			return fmt.Errorf("%w: mcp credential record for %q has unexpected previous_overlay", credentialstore.ErrVaultCorrupt, id)
		}
		if rec.PendingCommit.HadPreviousOverlay {
			if rec.PendingCommit.PreviousOverlay == nil || strings.TrimSpace(rec.PendingCommit.PreviousOverlay.ID) != id {
				return fmt.Errorf("%w: mcp credential record for %q has invalid previous_overlay", credentialstore.ErrVaultCorrupt, id)
			}
			if overlayHasInlineCredentials(*rec.PendingCommit.PreviousOverlay) {
				return fmt.Errorf("%w: mcp credential record for %q has unscrubbed previous_overlay", credentialstore.ErrVaultCorrupt, id)
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
