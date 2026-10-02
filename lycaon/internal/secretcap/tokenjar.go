package secretcap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

const tokenJarPurpose = "HTTP token jar"

// TokenJarRequest identifies the jar one http_request call opens.
type TokenJarRequest struct {
	ProjectID, ChatSessionID, SessionID, OperationID, Name string
}

// Token is one captured credential and the service that issued it.
type Token struct {
	Value string `json:"value"`
	// Origin is the destination identity of the response that carried the
	// token; placement anywhere else is a disclosure the secret screen decides.
	Origin string `json:"origin"`
	// OriginLabel is the presentation form of Origin.
	OriginLabel string `json:"origin_label"`
}

// TokenJar holds the tokens an HTTP session captured, each bound to its issuer.
type TokenJar struct {
	Name      string
	Reference string
	Tokens    map[string]Token
	id        string
	request   TokenJarRequest
	issuer    *Service
}

// Names returns the held token names in no particular order.
func (j *TokenJar) Names() []string {
	if j == nil || len(j.Tokens) == 0 {
		return nil
	}
	names := make([]string, 0, len(j.Tokens))
	for name := range j.Tokens {
		names = append(names, name)
	}
	return names
}

// Values returns the held token values in no particular order.
func (j *TokenJar) Values() []string {
	if j == nil || len(j.Tokens) == 0 {
		return nil
	}
	vals := make([]string, 0, len(j.Tokens))
	for _, token := range j.Tokens {
		vals = append(vals, token.Value)
	}
	return vals
}

// Lookup returns the token held under name.
func (j *TokenJar) Lookup(name string) (Token, bool) {
	if j == nil || j.Tokens == nil {
		return Token{}, false
	}
	token, ok := j.Tokens[name]
	return token, ok
}

// Set holds token under name.
func (j *TokenJar) Set(name string, token Token) {
	if j.Tokens == nil {
		j.Tokens = make(map[string]Token)
	}
	j.Tokens[name] = token
}

// OpenTokenJar loads the chat-visible jar and registers its values for screening.
// An absent jar starts empty in memory.
func (s *Service) OpenTokenJar(ctx context.Context, req TokenJarRequest) (*TokenJar, error) {
	req = normalizeTokenJarRequest(req)
	if !validJarName(req.Name) || req.ProjectID == "" || req.ChatSessionID == "" || req.SessionID == "" || req.OperationID == "" {
		return nil, ErrInvalidPut
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	row, found, err := s.jarRow(ctx, OriginTokenJar, req.ProjectID, req.ChatSessionID, req.Name)
	if err != nil {
		return nil, err
	}
	jar := &TokenJar{Name: req.Name, Tokens: make(map[string]Token), request: req, issuer: s}
	if !found {
		return jar, nil
	}
	if err := s.jarAccess(row, OriginTokenJar, ErrInvalidPut, req.ProjectID, req.ChatSessionID); err != nil {
		return nil, err
	}
	jar.id, jar.Reference = row.ID, secretmatch.ReferenceToken(row.ID)
	access := ResolveContext{ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID, ToolName: jarTool}
	current, hasCurrent, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if !hasCurrent {
		s.recordUse(ctx, row.ID, 0, UseUnavailable, access)
		return nil, ErrValueMissing
	}
	entry, ok := s.values.get(current.ID)
	if !ok {
		s.recordUse(ctx, row.ID, current.Version, UseUnavailable, access)
		return nil, ErrValueMissing
	}
	var tokens map[string]Token
	if err := json.Unmarshal([]byte(entry.Value), &tokens); err != nil {
		s.recordUse(ctx, row.ID, current.Version, UseUnavailable, access)
		return nil, fmt.Errorf("%w: stored token jar is unreadable", ErrValueMissing)
	}
	jar.Tokens = tokens
	s.recordUse(ctx, row.ID, current.Version, UseResolved, access)
	s.rememberJarValues(req.ChatSessionID, "token", row.ID, jar.Values())
	return jar, nil
}

// SaveTokenJar commits token changes and saves the operation receipt.
func (s *Service) SaveTokenJar(ctx context.Context, req TokenJarRequest, jar *TokenJar) (*Metadata, error) {
	if jar == nil {
		return nil, nil
	}
	req = normalizeTokenJarRequest(req)
	if jar.issuer != s || jar.request != req || jar.Tokens == nil {
		return nil, ErrInvalidPut
	}
	if len(jar.Tokens) == 0 {
		return nil, nil
	}
	defer s.invalidateScreening(ctx, req.ProjectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	s.rememberJarValues(req.ChatSessionID, "token", jar.id, jar.Values())
	if meta, found, err := s.tokenJarSavedOperation(ctx, req, jar); found || err != nil {
		return meta, err
	}
	row, found, err := s.tokenJarSaveTarget(ctx, req, jar)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(jar.Tokens)
	if err != nil {
		return nil, err
	}
	if err := validateValue(string(encoded)); err != nil {
		return nil, err
	}
	if !found {
		return s.createTokenJar(ctx, req, jar, string(encoded))
	}
	current, _, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	record := s.tokenJarSaveRecorder(ctx, req, row.ID)
	err = s.replaceOnto(ctx, row, current, true, CustodyHost, string(encoded), record)
	if err != nil {
		return nil, err
	}
	s.screeningGeneration.Add(1)
	jar.id, jar.Reference = row.ID, secretmatch.ReferenceToken(row.ID)
	meta, err := s.metadataRow(ctx, row)
	return &meta, err
}

func (s *Service) createTokenJar(ctx context.Context, req TokenJarRequest, jar *TokenJar, serialized string) (*Metadata, error) {
	id := uuid.NewString()
	put := PutRequest{
		ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
		OperationID: req.OperationID, Name: req.Name, Purpose: tokenJarPurpose, Scope: ScopeChat,
		Origin: OriginTokenJar, Value: serialized,
	}
	if err := validatePut(put); err != nil {
		return nil, err
	}
	if err := s.createWithFirstVersion(ctx, id, put, s.now().UTC(), s.tokenJarSaveRecorder(ctx, req, id)); err != nil {
		return nil, err
	}
	s.screeningGeneration.Add(1)
	jar.id, jar.Reference = id, secretmatch.ReferenceToken(id)
	row, err := s.queries.GetManagedSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	meta, err := s.metadataRow(ctx, row)
	return &meta, err
}

func (s *Service) tokenJarSaveRecorder(ctx context.Context, req TokenJarRequest, secretID string) func(*db.Queries) error {
	return func(q *db.Queries) error {
		return q.CreateTokenJarSave(ctx, db.CreateTokenJarSaveParams{
			ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
			OperationID: req.OperationID, Name: req.Name,
			SecretID: nullable(secretID),
		})
	}
}

func (s *Service) tokenJarSavedOperation(ctx context.Context, req TokenJarRequest, jar *TokenJar) (*Metadata, bool, error) {
	save, err := s.queries.GetTokenJarSave(ctx, db.GetTokenJarSaveParams{
		ProjectID: req.ProjectID, SessionID: req.SessionID, OperationID: req.OperationID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !save.SecretID.Valid || save.SecretID.String == "" {
		return nil, true, nil
	}
	row, err := s.rowForReference(ctx, secretmatch.ReferenceToken(save.SecretID.String))
	if err != nil {
		return nil, true, err
	}
	jar.id, jar.Reference = row.ID, secretmatch.ReferenceToken(row.ID)
	meta, err := s.metadataRow(ctx, row)
	return &meta, true, err
}

func (s *Service) tokenJarSaveTarget(ctx context.Context, req TokenJarRequest, jar *TokenJar) (db.ManagedSecrets, bool, error) {
	var row db.ManagedSecrets
	var found bool
	var err error
	if jar.id == "" {
		row, found, err = s.jarRow(ctx, OriginTokenJar, req.ProjectID, req.ChatSessionID, req.Name)
		found = found && row.Scope == ScopeChat
	} else {
		row, err = s.rowForReference(ctx, secretmatch.ReferenceToken(jar.id))
		found = err == nil
	}
	if err == nil && found {
		err = s.jarAccess(row, OriginTokenJar, ErrInvalidPut, req.ProjectID, req.ChatSessionID)
	}
	return row, found, err
}

func normalizeTokenJarRequest(req TokenJarRequest) TokenJarRequest {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChatSessionID = strings.TrimSpace(req.ChatSessionID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.Name = strings.TrimSpace(req.Name)
	return req
}
