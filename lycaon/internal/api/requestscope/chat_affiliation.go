package requestscope

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// chatAffiliation names the focused chat and the turn live when the change
// lands. A chat the host cannot place leaves the change unaffiliated; a
// person's text is never refused for its affiliation.
func ChatAffiliation(ctx context.Context, sessions session.Store, projectID, sessionID string) (string, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", 0, nil
	}
	sess, err := sessions.Get(ctx, sessionID)
	if errors.Is(err, store.ErrSessionNotFound) || (err == nil && sess == nil) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if projectID != "" && sess.ProjectID != projectID {
		return "", 0, nil
	}
	if sess.Status != wire.SessionStatusBusy || sess.CurrentTurn < 1 {
		return sessionID, 0, nil
	}
	return sessionID, sess.CurrentTurn, nil
}
