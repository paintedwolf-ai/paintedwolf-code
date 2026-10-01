package secretcap

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func jarRequest(operation string) CookieJarRequest {
	return CookieJarRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: operation, Name: "registry",
	}
}

func TestCookieJarPersistsAsAManagedSecretAndScreensItsValues(t *testing.T) {
	service, values, remembered := testService(t)
	site, err := url.Parse("http://localhost:5555/api/v1/login")
	testutil.FailErr(t, "parse url", err)

	jar, err := service.OpenCookieJar(t.Context(), jarRequest("call-1"))
	testutil.FailErr(t, "open fresh jar", err)
	if jar.Reference != "" || len(jar.Store.Snapshot()) != 0 {
		t.Fatalf("fresh jar = %+v", jar)
	}
	if meta, err := service.SaveCookieJar(t.Context(), jarRequest("call-1"), jar); err != nil || meta != nil {
		t.Fatalf("saving an untouched jar minted a capability: meta=%v err=%v", meta, err)
	}

	jar.Store.SetCookies(site, []*http.Cookie{{Name: "session", Value: "cookie-value-1", Path: "/"}})
	meta, err := service.SaveCookieJar(t.Context(), jarRequest("call-1"), jar)
	testutil.FailErr(t, "save jar", err)
	if meta == nil || meta.Origin != OriginCookieJar || meta.Scope != ScopeChat || meta.Name != "registry" || meta.Version != 1 {
		t.Fatalf("jar metadata = %+v", meta)
	}
	stored, ok := values.Get(currentValueID(t, service, meta.Reference))
	if !ok || !strings.Contains(stored.Value(), "cookie-value-1") {
		t.Fatal("jar value was not stored")
	}
	found := false
	for _, item := range *remembered {
		if item.Secret == "cookie-value-1" && item.Reference == "" && item.NonDisclosable {
			found = true
		}
	}
	if !found {
		t.Fatalf("cookie value was not admitted to screening: %+v", *remembered)
	}

	reopened, err := service.OpenCookieJar(t.Context(), jarRequest("call-2"))
	testutil.FailErr(t, "reopen jar", err)
	if reopened.Reference != meta.Reference {
		t.Fatalf("reopened jar reference = %q want %q", reopened.Reference, meta.Reference)
	}
	sent := reopened.Store.Cookies(site)
	if len(sent) != 1 || sent[0].Value != "cookie-value-1" {
		t.Fatalf("reopened jar cookies = %+v", sent)
	}
	reopened.Store.SetCookies(site, []*http.Cookie{{Name: "csrf", Value: "token-value-2", Path: "/"}})
	meta, err = service.SaveCookieJar(t.Context(), jarRequest("call-2"), reopened)
	testutil.FailErr(t, "save reopened jar", err)
	if meta == nil || meta.Version != 2 || meta.UseCount != 1 {
		t.Fatalf("second save metadata = %+v", meta)
	}
	listed, err := service.List(t.Context(), testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "list", err)
	if len(listed) != 1 || listed[0].Origin != OriginCookieJar {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestCookieJarIsScopedToItsChatAndRevocable(t *testing.T) {
	service, _, _ := testService(t)
	site, err := url.Parse("https://example.test/")
	testutil.FailErr(t, "parse url", err)
	jar, err := service.OpenCookieJar(t.Context(), jarRequest("call-1"))
	testutil.FailErr(t, "open jar", err)
	jar.Store.SetCookies(site, []*http.Cookie{{Name: "session", Value: "cookie-value-1"}})
	meta, err := service.SaveCookieJar(t.Context(), jarRequest("call-1"), jar)
	testutil.FailErr(t, "save jar", err)

	other := jarRequest("call-9")
	other.ChatSessionID, other.SessionID = "root-2", "root-2"
	foreign, err := service.OpenCookieJar(t.Context(), other)
	testutil.FailErr(t, "open jar from another chat", err)
	if foreign.Reference != "" {
		t.Fatal("another chat saw this chat's jar")
	}

	if _, err := service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", meta.Reference); err != nil {
		t.Fatalf("agent could not revoke its own jar: %v", err)
	}
	fresh, err := service.OpenCookieJar(t.Context(), jarRequest("call-3"))
	testutil.FailErr(t, "open after revoke", err)
	if fresh.Reference != "" {
		t.Fatal("a revoked jar was reopened")
	}

	bad := jarRequest("call-4")
	bad.Name = "bad\x00name"
	if _, err := service.OpenCookieJar(t.Context(), bad); !errors.Is(err, ErrInvalidCookieJar) {
		t.Fatalf("control character in jar name accepted: %v", err)
	}
}
