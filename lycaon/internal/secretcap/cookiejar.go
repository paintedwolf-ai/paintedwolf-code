package secretcap

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

const (
	cookieJarPurpose = "HTTP cookie jar"
	// jarTool is the only jar consumer; uses are recorded under its name.
	jarTool         = "http_request"
	maxJarNameRunes = 64
)

// CookieJarRequest identifies the jar one http_request call opens.
type CookieJarRequest struct {
	ProjectID, ChatSessionID, SessionID, OperationID, Name string
}

// CookieJar binds a live cookie store to a capability after its first stored cookie.
type CookieJar struct {
	Name      string
	Reference string
	Store     *httpcookies.Store
	id        string
	request   CookieJarRequest
	issuer    *Service
}

// ErrInvalidCookieJar refuses a jar name outside its limits.
var ErrInvalidCookieJar = fmt.Errorf("%w: cookie jar name", ErrInvalidPut)

// OpenCookieJar loads the chat-visible jar and registers its values for screening.
// An absent jar starts empty in memory.
func (s *Service) OpenCookieJar(ctx context.Context, req CookieJarRequest) (*CookieJar, error) {
	req = normalizeCookieJarRequest(req)
	if !validJarName(req.Name) || req.ProjectID == "" || req.ChatSessionID == "" || req.SessionID == "" || req.OperationID == "" {
		return nil, ErrInvalidCookieJar
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	row, found, err := s.jarRow(ctx, OriginCookieJar, req.ProjectID, req.ChatSessionID, req.Name)
	if err != nil {
		return nil, err
	}
	jar := &CookieJar{Name: req.Name, Store: httpcookies.New(nil), request: req, issuer: s}
	if !found {
		return jar, nil
	}
	if err := s.jarAccess(row, OriginCookieJar, ErrInvalidCookieJar, req.ProjectID, req.ChatSessionID); err != nil {
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
	cookies, err := httpcookies.Decode([]byte(entry.Value))
	if err != nil {
		s.recordUse(ctx, row.ID, current.Version, UseUnavailable, access)
		return nil, fmt.Errorf("%w: stored cookie jar is unreadable", ErrValueMissing)
	}
	jar.Store = httpcookies.New(cookies)
	s.recordUse(ctx, row.ID, current.Version, UseResolved, access)
	s.rememberJarValues(req.ChatSessionID, "cookie", row.ID, jar.Store.Values())
	return jar, nil
}

// jarRow finds the live jar of origin named name that the chat can see,
// preferring the chat's own jar over a project one.
func (s *Service) jarRow(ctx context.Context, origin, projectID, chatSessionID, name string) (row db.ManagedSecrets, found bool, err error) {
	rows, err := s.queries.ListVisibleManagedSecrets(ctx, db.ListVisibleManagedSecretsParams{
		ProjectID: strings.TrimSpace(projectID), ChatSessionID: nullable(chatSessionID),
	})
	if err != nil {
		return db.ManagedSecrets{}, false, err
	}
	for _, candidate := range rows {
		if candidate.Origin != origin || candidate.Name != name || candidate.RevokedAt.Valid {
			continue
		}
		if !found || candidate.Scope == ScopeChat {
			row, found = candidate, true
		}
	}
	return row, found, nil
}

func normalizeCookieJarRequest(req CookieJarRequest) CookieJarRequest {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChatSessionID = strings.TrimSpace(req.ChatSessionID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.Name = strings.TrimSpace(req.Name)
	return req
}

func validJarName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= maxJarNameRunes && !strings.ContainsFunc(name, isControlRune)
}

// jarAccess admits a live, visible jar of origin; any other origin is wrongOrigin.
func (s *Service) jarAccess(row db.ManagedSecrets, origin string, wrongOrigin error, projectID, chatSessionID string) error {
	if row.Origin != origin {
		return wrongOrigin
	}
	if err := visible(row, projectID, chatSessionID); err != nil {
		return err
	}
	if row.RevokedAt.Valid {
		return ErrRevoked
	}
	if agentUseEnded(row.AgentUseEndsAt, s.now()) {
		return ErrAgentUseExpired
	}
	return nil
}

// rememberJarValues screens individual entries without substituting the whole jar.
func (s *Service) rememberJarValues(chatSessionID, entry, jarID string, values []string) {
	if s.remember == nil || strings.TrimSpace(chatSessionID) == "" || len(values) == 0 {
		return
	}
	items := make([]secretmatch.Remembered, 0, len(values))
	for _, value := range values {
		if utf8.RuneCountInString(value) < secretmatch.MinManagedSecretRunes {
			continue
		}
		items = append(items, secretmatch.Remembered{
			Secret: value, Name: entry, Origin: entry + " jar " + jarID,
			RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		})
	}
	if len(items) > 0 {
		s.remember(chatSessionID, items)
	}
}

func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f
}
