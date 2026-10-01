package secretcap

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCookieJarSaveRemainsBoundToOpeningAuthority(t *testing.T) {
	service, _, _ := testService(t)
	jar := openJar(t, service, "seed")
	jar.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "session-value"}})
	for _, field := range []string{"project", "root", "session", "operation", "name"} {
		req := jar.request
		switch field {
		case "project":
			req.ProjectID = "other"
		case "root":
			req.ChatSessionID = "root-2"
		case "session":
			req.SessionID = "root-2"
		case "operation":
			req.OperationID = "other"
		case "name":
			req.Name = "other"
		}
		if _, err := service.SaveCookieJar(t.Context(), req, jar); !errors.Is(err, ErrInvalidCookieJar) {
			t.Fatalf("%s identity change accepted: %v", field, err)
		}
	}
	meta := saveJar(t, service, jar)
	stale := openJar(t, service, "late")
	_, err := service.RevokeProject(t.Context(), jar.request.ProjectID, meta.Reference, testOwner(t, service))
	testutil.FailErr(t, "revoke while request is in flight", err)
	stale.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "late-value"}})
	if _, err := service.SaveCookieJar(t.Context(), stale.request, stale); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked save = %v", err)
	}
	fresh := openJar(t, service, "fresh")
	fresh.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "fresh-value"}})
	if saveJar(t, service, fresh).Reference == meta.Reference {
		t.Fatal("fresh exchange revived a revoked capability")
	}
}

func TestCookieJarDeadlineRefusesOpenAndInflightSave(t *testing.T) {
	service, _, _ := testService(t)
	jar := openJar(t, service, "seed")
	jar.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "session-value"}})
	meta := saveJar(t, service, jar)
	stale := openJar(t, service, "late")
	deadline := service.now().Add(time.Minute)
	_, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: jar.request.ProjectID, Reference: meta.Reference,
		AgentUseDeadline: &AgentUseDeadline{At: deadline.Format(time.RFC3339Nano)},
	})
	testutil.FailErr(t, "set deadline", err)
	service.now = func() time.Time { return deadline }
	stale.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "late-value"}})
	if _, err := service.SaveCookieJar(t.Context(), stale.request, stale); !errors.Is(err, ErrAgentUseExpired) {
		t.Fatalf("save past deadline = %v", err)
	}
	if _, err := service.OpenCookieJar(t.Context(), jarRequest("fresh")); !errors.Is(err, ErrAgentUseExpired) {
		t.Fatalf("opening expired jar bypassed deadline: %v", err)
	}
}

func TestCookieJarProjectPromotionAndNameUniqueness(t *testing.T) {
	service, _, _ := testService(t)
	jar := openJar(t, service, "seed")
	jar.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "project-value"}})
	meta := saveJar(t, service, jar)
	scope := ScopeProject
	_, err := service.Update(t.Context(), UpdateRequest{ProjectID: jar.request.ProjectID, Reference: meta.Reference, Scope: &scope})
	testutil.FailErr(t, "promote project jar", err)
	other := jarRequest("worker-other-root")
	other.ChatSessionID, other.SessionID = "root-2", "root-2"
	second, err := service.OpenCookieJar(t.Context(), other)
	testutil.FailErr(t, "open project jar from other chat", err)
	first := openJar(t, service, "worker-first-root")
	first.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "first", Value: "first-value"}})
	second.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "second", Value: "second-value"}})
	saveJar(t, service, first)
	saveJar(t, service, second)
	if got := openJar(t, service, "read"); got.Reference != meta.Reference || len(got.Store.Snapshot()) != 3 {
		t.Fatal("project jar was not shared and merged across chats")
	}
	other.Name, other.OperationID = "alternate", "alternate"
	alternate, err := service.OpenCookieJar(t.Context(), other)
	testutil.FailErr(t, "open alternate", err)
	alternate.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "alternate-value"}})
	altMeta := saveJar(t, service, alternate)
	_, err = service.Update(t.Context(), UpdateRequest{ProjectID: other.ProjectID, Reference: altMeta.Reference, Scope: &scope})
	testutil.FailErr(t, "promote alternate", err)
	name := "registry"
	if _, err := service.Update(t.Context(), UpdateRequest{ProjectID: other.ProjectID, Reference: altMeta.Reference, Name: &name}); err == nil {
		t.Fatal("rename created two live project jars with the same name")
	}
	name = strings.Repeat("a", maxJarNameRunes+1)
	if _, err := service.Update(t.Context(), UpdateRequest{ProjectID: other.ProjectID, Reference: altMeta.Reference, Name: &name}); !errors.Is(err, ErrInvalidCookieJar) {
		t.Fatalf("rename made jar inaccessible to its name validator: %v", err)
	}
}

func TestCookieJarScreeningRestoresIndividualRetiredValues(t *testing.T) {
	service, _, _ := testService(t)
	jar := openJar(t, service, "first")
	jar.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "old-session-value"}})
	saveJar(t, service, jar)
	later := openJar(t, service, "second")
	later.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "sid", Value: "new-session-value"}})
	saveJar(t, service, later)
	var restored []secretmatch.Remembered
	service.remember = func(_ string, items []secretmatch.Remembered) { restored = append(restored, items...) }
	testutil.FailErr(t, "reload screening", service.RememberProjectValues(t.Context(), jar.request.ProjectID, "root-2"))
	if len(restored) != 2 {
		t.Fatalf("restored screening entries=%d", len(restored))
	}
	for _, item := range restored {
		if item.Reference != "" || !item.NonDisclosable || strings.Contains(item.Secret, "cookies") {
			t.Fatal("screening offered a jar document as a credential")
		}
	}
	if restored[0].Secret != "old-session-value" || restored[1].Secret != "new-session-value" {
		t.Fatal("screening lost cookie version history")
	}
}

func TestCookieJarConcurrentProjectPromotionDoesNotWidenFreshChatJar(t *testing.T) {
	service, _, _ := testService(t)
	private := openJar(t, service, "private-exchange")
	other := jarRequest("project-exchange")
	other.ChatSessionID, other.SessionID = "root-2", "root-2"
	shared, err := service.OpenCookieJar(t.Context(), other)
	testutil.FailErr(t, "open other chat jar", err)
	shared.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "shared", Value: "shared-value"}})
	sharedMeta := saveJar(t, service, shared)
	scope := ScopeProject
	_, err = service.Update(t.Context(), UpdateRequest{ProjectID: other.ProjectID, Reference: sharedMeta.Reference, Scope: &scope})
	testutil.FailErr(t, "promote while private exchange is in flight", err)
	private.Store.SetCookies(jarSite(t), []*http.Cookie{{Name: "private", Value: "private-value"}})
	privateMeta := saveJar(t, service, private)
	if privateMeta.Scope != ScopeChat || privateMeta.Reference == sharedMeta.Reference {
		t.Fatal("in-flight chat cookies were widened into a newly visible project jar")
	}
	other.OperationID = "read-shared"
	readShared, err := service.OpenCookieJar(t.Context(), other)
	testutil.FailErr(t, "read shared jar", err)
	if _, leaked := readShared.Store.Lookup(jarSite(t), "private"); leaked {
		t.Fatal("another chat received the private exchange's cookie")
	}
	if own := openJar(t, service, "read-private"); own.Reference != privateMeta.Reference {
		t.Fatal("chat jar did not take precedence over project jar")
	}
}
