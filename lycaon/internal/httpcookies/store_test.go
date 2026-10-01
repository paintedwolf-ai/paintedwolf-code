package httpcookies

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func names(cookies []*http.Cookie) []string {
	out := make([]string, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, c.Name)
	}
	return out
}

func TestStoreSendsCookiesBackToTheirOriginOnly(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "http://localhost:5555/api/v1/login"), []*http.Cookie{
		{Name: "session", Value: "abc123", Path: "/"},
	})
	got := store.Cookies(mustURL(t, "http://localhost:5555/api/v1/user"))
	if len(got) != 1 || got[0].Name != "session" || got[0].Value != "abc123" {
		t.Fatalf("cookies = %+v", got)
	}
	// Ports are not part of the cookie origin, so this is the same host.
	if got := store.Cookies(mustURL(t, "http://localhost:6666/api/v1/user")); len(got) != 1 {
		t.Fatalf("port-only difference dropped cookies: %+v", got)
	}
	if got := store.Cookies(mustURL(t, "http://other.test/api/v1/user")); len(got) != 0 {
		t.Fatalf("cookie leaked to another host: %+v", got)
	}
	sent, stored := store.Counts()
	if sent != 2 || stored != 1 {
		t.Fatalf("counts sent=%d stored=%d", sent, stored)
	}
}

func TestStoreAppliesDomainPathAndSecureRules(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://app.example.com/account/settings"), []*http.Cookie{
		{Name: "wide", Value: "1", Domain: ".example.com", Path: "/"},
		{Name: "deep", Value: "2"},
		{Name: "locked", Value: "3", Path: "/", Secure: true},
		{Name: "foreign", Value: "4", Domain: "other.com"},
		{Name: "suffix", Value: "5", Domain: "com"},
	})
	if got := names(store.Cookies(mustURL(t, "https://api.example.com/v1"))); len(got) != 1 || got[0] != "wide" {
		t.Fatalf("subdomain cookies = %v, want the domain cookie only", got)
	}
	if got := names(store.Cookies(mustURL(t, "https://app.example.com/account/profile"))); len(got) != 3 {
		t.Fatalf("same-directory cookies = %v", got)
	}
	if got := names(store.Cookies(mustURL(t, "https://app.example.com/other"))); len(got) != 2 {
		t.Fatalf("default-path cookie escaped its directory: %v", got)
	}
	if got := names(store.Cookies(mustURL(t, "http://app.example.com/account/profile"))); len(got) != 2 {
		t.Fatalf("secure cookie sent over http: %v", got)
	}
}

func TestStoreExpiresAndDeletesCookies(t *testing.T) {
	store := New(nil)
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	u := mustURL(t, "https://example.com/")
	store.SetCookies(u, []*http.Cookie{
		{Name: "short", Value: "1", MaxAge: 60},
		{Name: "gone", Value: "2", Expires: base.Add(-time.Hour)},
		{Name: "keep", Value: "3"},
	})
	if got := names(store.Cookies(u)); len(got) != 2 {
		t.Fatalf("live cookies = %v", got)
	}
	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	if got := names(store.Cookies(u)); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("after expiry cookies = %v", got)
	}
	store.SetCookies(u, []*http.Cookie{{Name: "keep", Value: "", MaxAge: -1}})
	if got := store.Cookies(u); len(got) != 0 {
		t.Fatalf("deleted cookie still sent: %+v", got)
	}
}

func TestStoreRoundTripsThroughItsEncoding(t *testing.T) {
	store := New(nil)
	store.SetCookies(mustURL(t, "https://example.com/a/b"), []*http.Cookie{
		{Name: "token", Value: "value-1", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode},
	})
	if !store.Changed() {
		t.Fatal("storing a cookie did not mark the jar changed")
	}
	raw, err := store.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cookies, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	reloaded := New(cookies)
	if reloaded.Changed() {
		t.Fatal("a freshly loaded jar reported changes")
	}
	got := reloaded.Cookies(mustURL(t, "https://example.com/x"))
	if len(got) != 1 || got[0].Value != "value-1" {
		t.Fatalf("reloaded cookies = %+v", got)
	}
	if values := reloaded.Values(); len(values) != 1 || values[0] != "value-1" {
		t.Fatalf("values = %v", values)
	}
	if cookies, err := Decode(nil); err != nil || cookies != nil {
		t.Fatalf("empty document = %v err=%v", cookies, err)
	}
}
