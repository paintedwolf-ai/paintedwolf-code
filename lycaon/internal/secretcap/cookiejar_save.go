package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SaveCookieJar atomically commits accepted cookie operations and their receipt.
// Only persistence is locked; unchanged values still receive an operation receipt.
func (s *Service) SaveCookieJar(ctx context.Context, req CookieJarRequest, jar *CookieJar) (*Metadata, error) {
	if jar == nil {
		return nil, nil
	}
	req = normalizeCookieJarRequest(req)
	if jar.issuer != s || jar.request != req || jar.Store == nil {
		return nil, ErrInvalidCookieJar
	}
	changes := jar.Store.Changes()
	if changes.Empty() {
		return nil, nil
	}
	defer s.invalidateScreening(ctx, req.ProjectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	// Screen received credentials even if persistence fails.
	s.rememberJarValues(req.ChatSessionID, "cookie", jar.id, changes.Values())
	if meta, found, err := s.cookieJarSavedOperation(ctx, req, jar); found || err != nil {
		return meta, err
	}
	row, found, err := s.cookieJarSaveTarget(ctx, req, jar)
	if err != nil {
		return nil, err
	}
	if !found {
		return s.createCookieJar(ctx, req, jar, changes.Merge(nil))
	}
	current, cookies, err := s.cookieJarCurrent(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	merged := changes.Merge(cookies)
	record := s.cookieJarSaveRecorder(ctx, req, row.ID)
	if merged.Changed() {
		encoded, encodeErr := merged.Encode()
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := validateValue(string(encoded)); err != nil {
			return nil, err
		}
		err = s.replaceOnto(ctx, row, current, true, string(encoded), record)
	} else {
		err = s.inTx(ctx, record)
	}
	if err != nil {
		return nil, err
	}
	s.screeningGeneration.Add(1)
	jar.id, jar.Reference = row.ID, secretmatch.ReferenceToken(row.ID)
	meta, err := s.metadataRow(ctx, row)
	return &meta, err
}

func (s *Service) cookieJarSaveTarget(ctx context.Context, req CookieJarRequest, jar *CookieJar) (db.ManagedSecrets, bool, error) {
	var row db.ManagedSecrets
	var found bool
	var err error
	if jar.id == "" {
		// Only a concurrently minted chat jar can share an initially empty exchange.
		row, found, err = s.jarRow(ctx, OriginCookieJar, req.ProjectID, req.ChatSessionID, req.Name)
		found = found && row.Scope == ScopeChat
	} else {
		// An open jar stays bound to its capability even if renamed or revoked.
		row, err = s.rowForReference(ctx, secretmatch.ReferenceToken(jar.id))
		found = err == nil
	}
	if err == nil && found {
		err = s.jarAccess(row, OriginCookieJar, ErrInvalidCookieJar, req.ProjectID, req.ChatSessionID)
	}
	return row, found, err
}

func (s *Service) cookieJarCurrent(ctx context.Context, id string) (db.ManagedSecretVersions, []httpcookies.Cookie, error) {
	current, found, err := s.currentVersion(ctx, id)
	if err != nil {
		return current, nil, err
	}
	if !found {
		return current, nil, ErrValueMissing
	}
	raw, ok := s.values.Get(current.ID)
	if !ok {
		return current, nil, ErrValueMissing
	}
	cookies, err := httpcookies.Decode([]byte(raw.Value()))
	if err != nil {
		return current, nil, fmt.Errorf("%w: stored cookie jar is unreadable", ErrValueMissing)
	}
	return current, cookies, nil
}

func (s *Service) createCookieJar(ctx context.Context, req CookieJarRequest, jar *CookieJar, merged *httpcookies.Store) (*Metadata, error) {
	if len(merged.Snapshot()) == 0 {
		return nil, s.inTx(ctx, s.cookieJarSaveRecorder(ctx, req, ""))
	}
	encoded, err := merged.Encode()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	put := PutRequest{
		ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
		OperationID: req.OperationID, Name: req.Name, Purpose: cookieJarPurpose, Scope: ScopeChat,
		Origin: OriginCookieJar, Value: string(encoded),
	}
	if err := validatePut(put); err != nil {
		return nil, err
	}
	if err := s.createWithFirstVersion(ctx, id, put, s.now().UTC(), s.cookieJarSaveRecorder(ctx, req, id)); err != nil {
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

func (s *Service) cookieJarSaveRecorder(ctx context.Context, req CookieJarRequest, id string) func(*db.Queries) error {
	return func(q *db.Queries) error {
		return q.CreateCookieJarSave(ctx, db.CreateCookieJarSaveParams{
			ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
			OperationID: req.OperationID, Name: req.Name, SecretID: nullable(id),
		})
	}
}

func (s *Service) cookieJarSavedOperation(ctx context.Context, req CookieJarRequest, jar *CookieJar) (*Metadata, bool, error) {
	saved, err := s.queries.GetCookieJarSave(ctx, db.GetCookieJarSaveParams{
		ProjectID: req.ProjectID, SessionID: req.SessionID, OperationID: req.OperationID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if saved.ChatSessionID != req.ChatSessionID || saved.Name != req.Name ||
		(saved.SecretID.Valid && jar.id != "" && jar.id != saved.SecretID.String) {
		return nil, true, ErrInvalidCookieJar
	}
	if !saved.SecretID.Valid {
		return nil, true, nil
	}
	row, err := s.rowForReference(ctx, secretmatch.ReferenceToken(saved.SecretID.String))
	if err != nil {
		return nil, true, err
	}
	if err := s.jarAccess(row, OriginCookieJar, ErrInvalidCookieJar, req.ProjectID, req.ChatSessionID); err != nil {
		return nil, true, err
	}
	jar.id, jar.Reference = row.ID, secretmatch.ReferenceToken(row.ID)
	meta, err := s.metadataRow(ctx, row)
	return &meta, true, err
}
