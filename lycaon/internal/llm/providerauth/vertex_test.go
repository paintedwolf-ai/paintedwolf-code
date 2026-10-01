package providerauth

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/oauth2"
)

func TestVertexADCResolutionRetriesAfterFailure(t *testing.T) {
	wantErr := errors.New("temporary ADC failure")
	attempts := 0
	token := newADCTokenSourceWithResolver(func(context.Context) (oauth2.TokenSource, error) {
		attempts++
		if attempts == 1 {
			return nil, wantErr
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "recovered"}), nil
	})

	if _, err := token(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("first token error = %v", err)
	}
	got, err := token(t.Context())
	testutil.FailErr(t, "second token", err)
	if got != "recovered" || attempts != 2 {
		t.Fatalf("token = %q attempts = %d", got, attempts)
	}
}
