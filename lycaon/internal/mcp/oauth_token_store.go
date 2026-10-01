package mcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
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
