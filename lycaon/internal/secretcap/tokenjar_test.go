package secretcap

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func tokenJarRequest(operation string) TokenJarRequest {
	return TokenJarRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: operation, Name: "api",
	}
}

// A token remembers the service that issued it across saves and reopens, so
// a later call can tell a same-service use from a disclosure.
func TestTokenJarKeepsEachTokensIssuer(t *testing.T) {
	service, values, _ := testService(t)
	ctx := t.Context()
	first := tokenJarRequest("op-1")
	jar, err := service.Jars.OpenTokenJar(ctx, first)
	testutil.FailErr(t, "open empty jar", err)
	jar.Set("session", Token{Value: "issued-by-auth-7731", Origin: "auth.example@abc", OriginLabel: "https://auth.example:443"})
	meta, err := service.Jars.SaveTokenJar(ctx, first, jar)
	testutil.FailErr(t, "save jar", err)
	if meta == nil || meta.Origin != OriginTokenJar {
		t.Fatalf("saved metadata = %+v, want a token jar capability", meta)
	}

	reopened, err := service.Jars.OpenTokenJar(ctx, tokenJarRequest("op-2"))
	testutil.FailErr(t, "reopen jar", err)
	token, ok := reopened.Lookup("session")
	if !ok || token != (Token{Value: "issued-by-auth-7731", Origin: "auth.example@abc", OriginLabel: "https://auth.example:443"}) {
		t.Fatalf("reopened token = %+v, want the issuer preserved", token)
	}

	restarted := NewWithStore(service.repository.handle, values, nil)
	testutil.FailErr(t, "restore protected evidence", restarted.Reconcile(ctx))
	assertDurableValues(t, restarted, "issued-by-auth-7731")
}

func TestOverlappingCapturesMergeByName(t *testing.T) {
	service, _, _ := testService(t)
	open := func(operation string) *TokenJar {
		t.Helper()
		jar, err := service.Jars.OpenTokenJar(t.Context(), tokenJarRequest(operation))
		testutil.FailErr(t, "open jar", err)
		return jar
	}
	first, second := open("first"), open("second")
	first.Set("a", Token{Value: "first-capture", Origin: "issuer-a"})
	second.Set("b", Token{Value: "second-capture", Origin: "issuer-b"})
	for _, jar := range []*TokenJar{first, second, first} {
		_, err := service.Jars.SaveTokenJar(t.Context(), jar.request, jar)
		testutil.FailErr(t, "commit capture", err)
	}
	merged := open("read")
	if merged.Tokens["a"] != first.Tokens["a"] || merged.Tokens["b"] != second.Tokens["b"] || merged.Reference != first.Reference {
		t.Fatal("overlapping captures lost a token or its issuer")
	}
}
