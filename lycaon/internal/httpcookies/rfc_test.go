package httpcookies

import (
	"net/http"
	"testing"
	"time"
)

func TestExplicitDomainReachesSubdomains(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "sid", Value: "v", Domain: "example.com", Path: "/"},
	})
	// RFC 6265 5.3-6: a non-empty Domain attribute clears host-only even when
	// it names the request host, so a root login reaches an API subdomain.
	if got := store.Cookies(mustURL(t, "https://api.example.com/")); len(got) != 1 {
		t.Fatalf("subdomain carried %d cookies, want the domain cookie", len(got))
	}
	if got := store.Cookies(mustURL(t, "https://example.org/")); len(got) != 0 {
		t.Fatalf("an unrelated domain carried %d cookies", len(got))
	}
}

func TestAbsentDomainStaysHostOnly(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "sid", Value: "v", Path: "/"}})
	if got := store.Cookies(mustURL(t, "https://api.example.com/")); len(got) != 0 {
		t.Fatalf("a host-only cookie reached a subdomain: %d", len(got))
	}
}

func TestReplayingTheSameCookieIsTrafficNotAChange(t *testing.T) {
	stored := Cookie{Name: "sid", Value: "v", Domain: "example.com", HostOnly: true, Path: "/", Created: time.Now()}
	store := New([]Cookie{stored})
	if store.Changed() {
		t.Fatal("a freshly loaded jar reported a change")
	}
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "sid", Value: "v", Path: "/"}})
	if store.Changed() {
		t.Fatal("an identical replay marked the jar changed, which mints a jar version per request")
	}
	if sent, storedCount := store.Counts(); sent != 0 || storedCount != 1 {
		t.Fatalf("counts = %d sent, %d stored; the replay is still traffic", sent, storedCount)
	}
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "sid", Value: "rotated", Path: "/"}})
	if !store.Changed() {
		t.Fatal("a rotated value did not mark the jar changed")
	}
}

func TestLoadingAnExpiredCookieIsNotAChange(t *testing.T) {
	store := New([]Cookie{{
		Name: "old", Value: "v", Domain: "example.com", HostOnly: true, Path: "/",
		Expires: time.Now().Add(-time.Hour), Created: time.Now().Add(-2 * time.Hour),
	}})
	if store.Changed() {
		t.Fatal("dropping an expired cookie on load marked the jar changed")
	}
	if len(store.Snapshot()) != 0 {
		t.Fatalf("expired cookie survived the load: %+v", store.Snapshot())
	}
}

func TestDeletingACookieTheJarNeverHeldChangesNothing(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "sid", Path: "/", MaxAge: -1}})
	if store.Changed() {
		t.Fatal("deleting an absent cookie marked the jar changed")
	}
}

func TestCookieNamePrefixesMustBeEarned(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		cookie *http.Cookie
		stored bool
	}{
		{"host prefix on http", "http://example.com/", &http.Cookie{Name: "__Host-sid", Value: "v", Secure: true, Path: "/"}, false},
		{"host prefix without secure", "https://example.com/", &http.Cookie{Name: "__Host-sid", Value: "v", Path: "/"}, false},
		{"host prefix with a domain", "https://example.com/", &http.Cookie{Name: "__Host-sid", Value: "v", Secure: true, Domain: "example.com", Path: "/"}, false},
		{"host prefix off root", "https://example.com/app/", &http.Cookie{Name: "__Host-sid", Value: "v", Secure: true, Path: "/app"}, false},
		{"host prefix earned", "https://example.com/", &http.Cookie{Name: "__Host-sid", Value: "v", Secure: true, Path: "/"}, true},
		{"secure prefix on http", "http://example.com/", &http.Cookie{Name: "__Secure-t", Value: "v", Secure: true, Path: "/"}, false},
		{"secure prefix earned", "https://example.com/", &http.Cookie{Name: "__Secure-t", Value: "v", Secure: true, Path: "/"}, true},
	}
	for _, tc := range cases {
		store := New(nil)
		store.SetCookies(mustURL(t, tc.origin), []*http.Cookie{tc.cookie})
		if got := len(store.Snapshot()) == 1; got != tc.stored {
			t.Fatalf("%s: stored = %v, want %v", tc.name, got, tc.stored)
		}
	}
}

func TestPersistedCookieWithoutAPathDoesNotMatchEverything(t *testing.T) {
	store := New([]Cookie{{Name: "sid", Value: "v", Domain: "example.com", HostOnly: true, Created: time.Now()}})
	if got := store.Snapshot(); len(got) != 1 || got[0].Path != "/" {
		t.Fatalf("loaded cookie = %+v, want a normalized root path", got)
	}
}

func TestLookupAndNamesDescribeTheJarWithoutSending(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://example.com/app/"), []*http.Cookie{
		{Name: "csrftoken", Value: "token-value", Path: "/"},
		{Name: "sessionid", Value: "session-value", Path: "/"},
	})
	value, ok := store.Lookup(mustURL(t, "https://example.com/app/submit"), "csrftoken")
	if !ok || value != "token-value" {
		t.Fatalf("Lookup = %q %v", value, ok)
	}
	if _, ok := store.Lookup(mustURL(t, "https://other.test/"), "csrftoken"); ok {
		t.Fatal("Lookup crossed to a host the cookie does not match")
	}
	names := store.Names()
	if len(names) != 2 || names[0] != "csrftoken" || names[1] != "sessionid" {
		t.Fatalf("Names = %v", names)
	}
	if sent, _ := store.Counts(); sent != 0 {
		t.Fatalf("a lookup counted as traffic: %d sent", sent)
	}
}
