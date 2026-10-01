package httpcookies

import (
	"net/http"
	"testing"
	"time"
)

func TestChangesKeepExplicitDeletionAndReplay(t *testing.T) {
	url := mustURL(t, "https://example.com/")
	for _, deletion := range []bool{false, true} {
		baseline := Cookie{Name: "sid", Value: "original", Domain: "example.com", Path: "/", HostOnly: true, Created: time.Now()}
		store := New([]Cookie{baseline})
		incoming := &http.Cookie{Name: "sid", Value: "original", Path: "/"}
		if deletion {
			store = New(nil)
			incoming.MaxAge = -1
		}
		store.SetCookies(url, []*http.Cookie{incoming})
		if store.Changed() || store.Changes().Empty() {
			t.Fatalf("deletion=%v: expected a recorded operation without a local change", deletion)
		}
		baseline.Value = "concurrent"
		merged := store.Changes().Merge([]Cookie{baseline})
		value, found := merged.Lookup(url, "sid")
		if deletion && found || !deletion && (!found || value != "original") {
			t.Fatalf("deletion=%v: merged found=%v value=%q", deletion, found, value)
		}
	}
}

func TestChangesPreserveAbsoluteExpiryAndDoNotCopyUnchangedCookies(t *testing.T) {
	store := New([]Cookie{{Name: "untouched", Value: "stale", Domain: "example.com", Path: "/", HostOnly: true}})
	store.now = func() time.Time { return time.Now().Add(-time.Hour) }
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "short", Value: "expired", MaxAge: 1}})
	merged := store.Changes().Merge(nil)
	if got := merged.Snapshot(); len(got) != 0 {
		t.Fatalf("merge revived an expired or untouched cookie: %+v", got)
	}
}

func TestChangesPreserveCookieIdentityAndAttributes(t *testing.T) {
	url := mustURL(t, "https://app.example.com/account/login")
	store := New(nil)
	store.SetCookies(url, []*http.Cookie{
		{Name: "sid", Value: "host", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode},
		{Name: "sid", Value: "domain", Domain: "example.com", Path: "/"},
	})
	created := time.Now().Add(-time.Hour)
	merged := store.Changes().Merge([]Cookie{
		{Name: "sid", Value: "old-host", Domain: "app.example.com", Path: "/account", HostOnly: true, Created: created},
		{Name: "other", Value: "concurrent", Domain: "example.com", Path: "/"},
	})
	if got := merged.Snapshot(); len(got) != 3 {
		t.Fatalf("merge lost a cookie identity: %+v", got)
	}
	host := merged.Snapshot()[0]
	if !host.HostOnly || !host.Secure || !host.HTTPOnly || host.SameSite != "strict" || !host.Created.Equal(created) {
		t.Fatalf("host attributes changed: %+v", host)
	}
	if cookies := merged.Cookies(mustURL(t, "https://api.example.com/")); len(cookies) != 2 {
		t.Fatalf("domain cookie selection = %+v", cookies)
	}
	if value, _ := merged.Lookup(url, "sid"); value != "host" {
		t.Fatalf("longest path did not win: %q", value)
	}
}

func TestChangesSnapshotDoesNotGrowAndScreensIntermediateCookies(t *testing.T) {
	store := New(nil)
	url := mustURL(t, "https://example.com/")
	store.SetCookies(url, []*http.Cookie{{Name: "sid", Value: "intermediate"}})
	first := store.Changes()
	store.SetCookies(url, []*http.Cookie{{Name: "sid", MaxAge: -1}})
	if value, _ := first.Merge(nil).Lookup(url, "sid"); value != "intermediate" {
		t.Fatal("changes snapshot mutated")
	}
	if values := store.Changes().Values(); len(values) != 2 || values[0] != "intermediate" {
		t.Fatalf("screening lost intermediate cookie: %v", values)
	}
}

func TestChangesDeletionOnlyRemovesItsDomainPathAndName(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://app.example.com/account/logout"), []*http.Cookie{{
		Name: "sid", Domain: "example.com", Path: "/account", MaxAge: -1,
	}})
	merged := store.Changes().Merge([]Cookie{
		{Name: "sid", Value: "remove", Domain: "example.com", Path: "/account"},
		{Name: "sid", Value: "root", Domain: "example.com", Path: "/"},
		{Name: "sid", Value: "host", Domain: "app.example.com", Path: "/account", HostOnly: true},
		{Name: "other", Value: "different-name", Domain: "example.com", Path: "/account"},
	})
	if got := merged.Snapshot(); len(got) != 3 {
		t.Fatalf("deletion crossed a cookie identity boundary: %+v", got)
	}
	for _, cookie := range merged.Snapshot() {
		if cookie.Value == "remove" {
			t.Fatal("deletion missed its target")
		}
	}
}
