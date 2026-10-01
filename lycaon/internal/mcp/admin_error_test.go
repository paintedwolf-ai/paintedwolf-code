package mcp

import (
	"errors"
	"io"
	"testing"
)

// IsAdminCode reports whether err carries the admin code, for tests in this directory.
func IsAdminCode(err error, code string) bool {
	var ae *AdminError
	return errors.As(err, &ae) && ae.Code == code
}

func TestAdminErrorCodeRoundTrip(t *testing.T) {
	err := AdminErrID(RejectRemoteRequiresHTTPS, "coropa")
	if !IsAdminCode(err, RejectRemoteRequiresHTTPS) {
		t.Fatalf("IsAdminCode = false for %+v", err)
	}
	if !errors.Is(err, err) {
		t.Fatal("errors.Is self")
	}
	if got := err.Error(); got != RejectRemoteRequiresHTTPS+": coropa" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestAdminWrapUnwrapsCause(t *testing.T) {
	err := AdminWrap(CodePersistFailed, io.ErrClosedPipe)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Unwrap lost cause: %v", err)
	}
	if !IsAdminCode(err, CodePersistFailed) {
		t.Fatalf("code = %v", err)
	}
}

func TestSyncFailureCodeUnreachable(t *testing.T) {
	if got := SyncFailureCode(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := SyncFailureCode(errors.New("register_tool: duplicate")); got != CodeSyncFailed {
		t.Fatalf("generic = %q", got)
	}
}

func TestPersistErrPreservesDuplicateID(t *testing.T) {
	err := persistErr(&DuplicateIDError{Layer: CatalogLayerUser, ID: "dup"})
	if !IsAdminCode(err, RejectDuplicateID) {
		t.Fatalf("code = %v", err)
	}
}

func TestOAuthErrCodesFailure(t *testing.T) {
	err := oauthErr(errors.New("protected resource metadata has no authorization servers"))
	if !IsAdminCode(err, CodeOAuthFailed) {
		t.Fatalf("code = %v", err)
	}
}

func TestOAuthErrMapsRegistrationRequired(t *testing.T) {
	err := oauthErr(ErrOAuthRegistrationRequired)
	if !IsAdminCode(err, CodeOAuthRegistrationRequired) {
		t.Fatalf("code = %v", err)
	}
}
