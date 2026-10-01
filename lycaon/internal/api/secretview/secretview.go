// Package secretview presents managed secrets and secret screening over HTTP:
// wire metadata, refusal mapping, prompt references, and text screens.
package secretview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func WriteError(responses *httpio.Responder, w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, secretcap.ErrInvalidReference):
		responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "secret id must be a UUID")
	case errors.Is(err, secretcap.ErrNotFound), errors.Is(err, secretcap.ErrNotVisible):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretNotFound, "managed secret not found")
	case errors.Is(err, secretcap.ErrRevoked):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretRevoked, "this secret is revoked, which is permanent")
	case errors.Is(err, secretcap.ErrAgentUseExpired):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretAgentUseExpired, "this secret has passed its agent-use deadline")
	case errors.Is(err, secretcap.ErrValueMissing):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretValueUnavailable, "this secret has no readable current value")
	case errors.Is(err, secretcap.ErrRevealUnavailable):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretRevealUnavailable, "authenticated reveal is unavailable in this app session")
	case errors.Is(err, secretcap.ErrRevealDenied):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretRevealDenied, "the reveal request expired, was already used, or could not be verified")
	case errors.Is(err, secretcap.ErrRevealChallengeNotFound):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretRevealChallengeNotFound, "the reveal request expired or was already used")
	case errors.Is(err, secretcap.ErrRevealChanged):
		responses.Fail(w, wire.ApiErrorCodeManagedSecretRevealChanged, "the secret changed while authentication was in progress; reveal it again")
	case errors.Is(err, secretcap.ErrInvalidUpdate), errors.Is(err, secretcap.ErrInvalidPut):
		responses.Fail(w, wire.ApiErrorCodeInvalidRequest, managedSecretRefusal(err))
	default:
		responses.InternalError(w, r, err)
	}
}

func managedSecretRefusal(err error) string {
	message := err.Error()
	if _, reason, found := strings.Cut(message, ": "); found {
		return reason
	}
	return message
}

func Metadata(item secretcap.Metadata) wire.ManagedSecret {
	return wire.ManagedSecret{
		Reference: item.Reference, ChatSessionID: item.ChatSessionID, ChatTitle: item.ChatTitle,
		ChatDeleted: item.ChatDeleted, Name: item.Name,
		Purpose: item.Purpose, Scope: item.Scope, Origin: item.Origin, Format: item.Format,
		EntropyBits: item.EntropyBits, CreatedAt: item.CreatedAt,
		AgentUseEndsAt: item.AgentUseEndsAt, State: item.State, Version: item.Version,
		ValueReplacedAt: item.ValueReplacedAt, LastUsedAt: item.LastUsedAt, UseCount: item.UseCount,
		LastRevealedAt: item.LastRevealedAt, RevealCount: item.RevealCount,
	}
}

const maxPromptSecretReferences = 32

func ReferenceBlock(store session.Store, secrets *secretcap.Service, ctx context.Context, sessionID string, refs []wire.PromptSecretReferencePart) (string, error) {
	if len(refs) == 0 {
		return "", nil
	}
	sess, err := store.Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	lines, err := ReferenceLines(store, secrets, ctx, sess, refs)
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func ReferenceLines(store session.Store, secrets *secretcap.Service,
	ctx context.Context,
	sess *wire.Session,
	refs []wire.PromptSecretReferencePart,
) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > maxPromptSecretReferences {
		return nil, fmt.Errorf("at most %d managed secret references allowed", maxPromptSecretReferences)
	}
	if secrets == nil {
		return nil, fmt.Errorf("managed secret storage not configured")
	}
	chatSessionID := session.RootSessionID(ctx, store, sess.ID)
	seen := make(map[string]struct{}, len(refs))
	lines := make([]string, 0, len(refs))
	for _, ref := range refs {
		reference := strings.TrimSpace(ref.Reference)
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		meta, err := secrets.Describe(ctx, sess.ProjectID, chatSessionID, reference)
		if err != nil {
			return nil, err
		}
		switch meta.State {
		case secretcap.StateActive:
		case secretcap.StateRevoked:
			return nil, secretcap.ErrRevoked
		case secretcap.StateAgentUseExpired:
			return nil, secretcap.ErrAgentUseExpired
		case secretcap.StateUnavailable:
			return nil, secretcap.ErrValueMissing
		default:
			return nil, secretcap.ErrValueMissing
		}
		seen[reference] = struct{}{}
		line := fmt.Sprintf("Managed secret %q: %s", meta.Name, meta.Reference)
		lines = append(lines, line)
	}
	return lines, nil
}

func ScreenText(screener *secretspan.Screener, ctx context.Context, text string) *wire.SecretScreen {
	if screener == nil {
		return nil
	}
	result := screener.Screen(ctx, text)
	if result == nil {
		return nil
	}
	screen := &wire.SecretScreen{
		Truncated: result.Truncated, ScreenedBytes: result.ScreenedBytes,
		CatalogVersion: result.CatalogVersion, Spans: make([]wire.SecretSpan, 0, len(result.Spans)),
	}
	for _, span := range result.Spans {
		screen.Spans = append(screen.Spans, wire.SecretSpan{
			Start: span.Start, End: span.End, State: wire.SecretSpanState(span.State),
			RuleID: span.RuleID, RuleTitle: span.RuleTitle,
			Reference: span.Reference, Shape: span.Shape,
		})
	}
	return screen
}

// ProjectContext snapshots the evidence people see when they review project
// text: every live capability by reference, whichever chat it serves.
func ProjectContext(secrets *secretcap.Service, ctx context.Context, projectID string) (context.Context, string, error) {
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{ProjectID: projectID})
	if secrets == nil {
		return ctx, "", nil
	}
	values, err := secrets.ReviewScreeningValues(ctx, projectID)
	if err != nil {
		return ctx, "", err
	}
	digest := sha256.New()
	if err := json.NewEncoder(digest).Encode(values); err != nil { // #nosec G117 -- Values are encoded directly into a SHA-256 digest.
		return ctx, "", err
	}
	return secretmatch.WithScreeningValues(ctx, values), hex.EncodeToString(digest.Sum(nil)), nil
}
