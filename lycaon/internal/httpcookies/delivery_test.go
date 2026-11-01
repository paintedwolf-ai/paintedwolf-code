package httpcookies

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCookieDeliveryWithholdingPreservesProtectedIssuerAndAttributes(t *testing.T) {
	target, _ := url.Parse("http://service.invalid/private")
	legacy, err := Decode([]byte(`{"cookies":[{"name":"session","value":"x","domain":"service.invalid","host_only":true,"path":"/","http_only":true,"same_site":"lax","created":"2026-01-01T00:00:00Z"}]}`))
	testutil.FailErr(t, "decode cookie state", err)
	for _, issuer := range []string{"", "retained_object:original"} {
		t.Run(issuer, func(t *testing.T) {
			legacy[0].NativeIssuer = issuer
			store := New(legacy)
			before := store.Snapshot()
			store.SetDelivery("retained_object:replacement", func(Cookie) bool { return false })
			if got := store.Cookies(target); len(got) != 0 {
				t.Fatal("withheld cookie reached request", got)
			}
			if value, held := store.Lookup(target, "session"); !held || value != "" {
				t.Fatal("withheld reference must substitute empty bytes while remaining held")
			}
			if !reflect.DeepEqual(store.Selected(target), before) || !reflect.DeepEqual(store.Snapshot(), before) || store.Changed() || !store.Changes().Empty() {
				t.Fatal("delivery changed protected cookie state")
			}
			raw, err := store.Encode()
			testutil.FailErr(t, "encode cookie state", err)
			restored, err := Decode(raw)
			testutil.FailErr(t, "decode cookie state", err)
			if !reflect.DeepEqual(restored, before) {
				t.Fatal("issuer did not survive encode/decode")
			}
			store.SetDelivery("retained_object:replacement", func(Cookie) bool { return true })
			if got := store.Cookies(target); len(got) != 1 || got[0].Value != "x" {
				t.Fatal("approved delivery lost short cookie", got)
			}
			if value, held := store.Lookup(target, "session"); !held || value != "x" {
				t.Fatal("approved reference lost value")
			}
			if !reflect.DeepEqual(store.Snapshot(), before) {
				t.Fatal("approval rewrote issuer")
			}
		})
	}
}

func TestCookieIssuerChangesOnlyThroughAcceptedResponseOperations(t *testing.T) {
	target, _ := url.Parse("http://service.invalid/private")
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	original := Cookie{Name: "session", Value: "x", Domain: "service.invalid", HostOnly: true, Path: "/", HTTPOnly: true, SameSite: "lax", Created: created, NativeIssuer: "retained_object:original"}
	concurrent := Cookie{Name: "other", Value: "y", Domain: "service.invalid", Path: "/", Created: created, NativeIssuer: "retained_object:concurrent"}
	store := New([]Cookie{original})
	store.SetDelivery("retained_object:replacement", nil)
	store.SetCookies(target, []*http.Cookie{{Name: "session", Value: "x", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}})
	if !store.Changed() {
		t.Fatal("issuer-only reissuance was treated as identical")
	}
	merged := store.Changes().Merge([]Cookie{original, concurrent})
	want := original
	want.NativeIssuer = "retained_object:replacement"
	got := merged.Snapshot()
	if len(got) != 2 || got[0] != concurrent || got[1] != want {
		t.Fatalf("concurrent response merge = %+v", got)
	}
	ordinary := New([]Cookie{want})
	ordinary.SetDelivery("", nil)
	ordinary.SetCookies(target, []*http.Cookie{{Name: "session", Value: "x", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}})
	if !ordinary.Changed() || ordinary.Snapshot()[0].NativeIssuer != "" {
		t.Fatal("actual ordinary response did not replace native issuer")
	}
	deleted := New([]Cookie{original})
	deleted.SetDelivery("retained_object:replacement", nil)
	deleted.SetCookies(target, []*http.Cookie{{Name: "session", Path: "/", MaxAge: -1}})
	remaining := deleted.Changes().Merge(got).Snapshot()
	if len(remaining) != 1 || remaining[0] != concurrent {
		t.Fatal("cookie deletion damaged unrelated issuance", remaining)
	}
}
