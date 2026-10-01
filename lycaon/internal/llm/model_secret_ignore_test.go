package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestModelScreenRefusesAnExceptionWithdrawnDuringReview(t *testing.T) {
	matcher := modelScreenMatcher(t)
	hits := matcher.ScreenContext(t.Context(), modelScreenGitHubToken)
	if len(hits) != 1 {
		t.Fatal("fixture was not detected")
	}
	active := true
	matcher.SetIgnoredSource(func(ctx context.Context) map[secretmatch.SecretFingerprint]bool {
		return map[secretmatch.SecretFingerprint]bool{hits[0].Fingerprint: active && secretmatch.AskAttributionFrom(ctx).ProjectID == "proj-1"}
	})
	asked := false
	screen := NewModelSecretScreen(matcher, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		asked = true
		active = false
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	request := modelSecretRequest()
	request.Messages[0].Content += "\nAWS_ACCESS_KEY_ID=AKIAQYJK5TXV4NZR7SGB"
	_, err := screen.Screen(t.Context(), ScreenDestination{ID: "provider"}, request)
	if !asked || !errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatalf("stale release: asked=%v err=%v", asked, err)
	}
}
