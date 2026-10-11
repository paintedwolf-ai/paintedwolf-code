package naming

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SetTitle overwrites the display title and publishes SessionEvent.
func (m *Service) SetTitle(ctx context.Context, id, rawTitle string) (*wire.Session, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, store.ErrSessionNotFound
	}
	title, err := NormalizeDisplayTitle(rawTitle)
	if err != nil {
		return nil, err
	}
	if err := m.store.UpdateSession(ctx, id, func(sess *wire.Session) {
		sess.Title = title
	}); err != nil {
		return nil, err
	}
	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m.PublishSession(ctx, sess, title)
	return sess, nil
}

// MaxDisplayTitleRunes is the user-rename max for session titles (OpenAPI maxLength).
const MaxDisplayTitleRunes = 80

// ErrInvalidDisplayTitle is returned when a rename commit fails validation.
var ErrInvalidDisplayTitle = errors.New("invalid session title")

// NormalizeDisplayTitle trims and validates a user-set session title.
// Empty after trim, over-long, or control characters (\n / \r / NUL) are rejected.
func NormalizeDisplayTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", ErrInvalidDisplayTitle
	}
	if utf8.RuneCountInString(title) > MaxDisplayTitleRunes {
		return "", ErrInvalidDisplayTitle
	}
	if strings.ContainsAny(title, "\n\r\x00") {
		return "", ErrInvalidDisplayTitle
	}
	return title, nil
}
