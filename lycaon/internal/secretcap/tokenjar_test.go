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
	jar, err := service.OpenTokenJar(ctx, first)
	testutil.FailErr(t, "open empty jar", err)
	jar.Set("session", Token{Value: "issued-by-auth-7731", Origin: "auth.example@abc", OriginLabel: "https://auth.example:443"})
	meta, err := service.SaveTokenJar(ctx, first, jar)
	testutil.FailErr(t, "save jar", err)
	if meta == nil || meta.Origin != OriginTokenJar {
		t.Fatalf("saved metadata = %+v, want a token jar capability", meta)
	}

	reopened, err := service.OpenTokenJar(ctx, tokenJarRequest("op-2"))
	testutil.FailErr(t, "reopen jar", err)
	token, ok := reopened.Lookup("session")
	if !ok || token != (Token{Value: "issued-by-auth-7731", Origin: "auth.example@abc", OriginLabel: "https://auth.example:443"}) {
		t.Fatalf("reopened token = %+v, want the issuer preserved", token)
	}

	restarted := NewWithStore(service.handle, values, nil)
	testutil.FailErr(t, "restore protected evidence", restarted.Reconcile(ctx))
	assertDurableValues(t, restarted, "issued-by-auth-7731")
}
