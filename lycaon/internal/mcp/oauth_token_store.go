package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

const (
	oauthRecordContext = "mcp oauth"
)

// OAuthTokenRecord is the MCP Authorization token set for one provider, stored
// as one JSON string in the credential vault.
type OAuthTokenRecord struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Resource     string    `json:"resource,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	AuthURL      string    `json:"auth_url,omitempty"`
}

// OAuthTokenStore persists MCP OAuth tokens in the encrypted credential vault.
type OAuthTokenStore struct {
	store *credentialstore.Store
}

// DefaultSlot describes where MCP OAuth tokens live on this platform.
func DefaultSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(dir),
		Namespace: credentialstore.NamespaceMCPOAuth,
		Context:   oauthRecordContext,
	}, nil
}

// NewOAuthTokenStore loads tokens from the encrypted credential vault.
func NewOAuthTokenStore() (*OAuthTokenStore, error) {
	slot, err := DefaultSlot()
	if err != nil {
		return nil, err
	}
	inner, err := credentialstore.Open(slot, nil)
	if err != nil {
		return nil, err
	}
	return &OAuthTokenStore{store: inner}, nil
}

// NewOAuthTokenStoreAt opens an empty store at path with a development identity file.
func NewOAuthTokenStoreAt(path string) *OAuthTokenStore {
	return &OAuthTokenStore{store: credentialstore.NewEmpty(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceMCPOAuth, Context: oauthRecordContext,
	}, nil)}
}

// Get returns a stored token record.
func (s *OAuthTokenStore) Get(providerID string) (OAuthTokenRecord, bool) {
	raw, ok := s.store.Get(providerID)
	if !ok {
		return OAuthTokenRecord{}, false
	}
	var rec OAuthTokenRecord
	if err := json.Unmarshal([]byte(raw.Value()), &rec); err != nil {
		// Invalid records cannot establish an authenticated session.
		return OAuthTokenRecord{}, false
	}
	return rec, true
}

// SignedIn reports whether an access or refresh token is present.
func (s *OAuthTokenStore) SignedIn(providerID string) bool {
	rec, ok := s.Get(providerID)
	if !ok {
		return false
	}
	return rec.AccessToken != "" || rec.RefreshToken != ""
}

// AccessToken returns a non-expired access token when available.
func (s *OAuthTokenStore) AccessToken(providerID string) string {
	rec, ok := s.Get(providerID)
	if !ok || rec.AccessToken == "" {
		return ""
	}
	if !rec.Expiry.IsZero() && time.Now().After(rec.Expiry.Add(-30*time.Second)) {
		return ""
	}
	return rec.AccessToken
}

// Put stores tokens for providerID.
func (s *OAuthTokenStore) Put(providerID string, rec OAuthTokenRecord) error {
	encoded, err := json.Marshal(rec) // #nosec G117 -- the encrypted credential record holds OAuth token fields
	if err != nil {
		return fmt.Errorf("encode %s record: %w", oauthRecordContext, err)
	}
	return s.store.Set(providerID, string(encoded))
}

// Delete clears tokens for providerID.
func (s *OAuthTokenStore) Delete(providerID string) error {
	return s.store.Delete(providerID)
}

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneProviderOverlay(ov MCPProviderOverlay) MCPProviderOverlay {
	out := MCPProviderOverlay{
		ID:               ov.ID,
		URL:              cloneStringPtr(ov.URL),
		Command:          cloneStringPtr(ov.Command),
		Token:            cloneStringPtr(ov.Token),
		Version:          cloneStringPtr(ov.Version),
		Recipe:           cloneStringPtr(ov.Recipe),
		CredentialWire:   cloneStringPtr(ov.CredentialWire),
		CredentialHeader: cloneStringPtr(ov.CredentialHeader),
	}
	if ov.Args != nil {
		args := append([]string(nil), (*ov.Args)...)
		out.Args = &args
	}
	if ov.Env != nil {
		env := cloneStringMap(*ov.Env)
		out.Env = &env
	}
	if ov.Headers != nil {
		hdrs := cloneStringMap(*ov.Headers)
		out.Headers = &hdrs
	}
	if ov.Enabled != nil {
		en := *ov.Enabled
		out.Enabled = &en
	}
	if ov.ToolLoading != nil {
		tl := *ov.ToolLoading
		out.ToolLoading = &tl
	}
	return out
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

func applyUpdateToCredentials(prev StaticCredentialRecord, req api.UpdateMcpProviderRequest) (StaticCredentialRecord, bool) {
	next := StaticCredentialRecord{
		Token:          prev.Token,
		Headers:        cloneStringMap(prev.Headers),
		Env:            cloneStringMap(prev.Env),
		Migrated:       true,
		TokenCleared:   prev.TokenCleared,
		HeadersCleared: prev.HeadersCleared,
		EnvCleared:     prev.EnvCleared,
	}
	changed := false
	if req.URL != nil && strings.TrimSpace(*req.URL) != "" && req.Env == nil && (len(next.Env) > 0 || next.EnvCleared) {
		next.Env = nil
		next.EnvCleared = false
		changed = true
	}
	if req.Command != nil && strings.TrimSpace(*req.Command) != "" {
		if req.Token == nil && (next.Token != "" || next.TokenCleared) {
			next.Token = ""
			next.TokenCleared = false
			changed = true
		}
		if req.Headers == nil && (len(next.Headers) > 0 || next.HeadersCleared) {
			next.Headers = nil
			next.HeadersCleared = false
			changed = true
		}
	}
	if req.Token != nil {
		tok := strings.TrimSpace(*req.Token)
		next.Token = tok
		next.TokenCleared = tok == ""
		changed = true
	}
	if req.Headers != nil {
		next.Headers = cloneStringMap(*req.Headers)
		if len(next.Headers) == 0 {
			next.Headers = nil
			next.HeadersCleared = true
		} else {
			next.HeadersCleared = false
		}
		changed = true
	}
	if req.Env != nil {
		next.Env = cloneStringMap(*req.Env)
		if len(next.Env) == 0 {
			next.Env = nil
			next.EnvCleared = true
		} else {
			next.EnvCleared = false
		}
		changed = true
	}
	return normalizeRecord(next), changed
}
