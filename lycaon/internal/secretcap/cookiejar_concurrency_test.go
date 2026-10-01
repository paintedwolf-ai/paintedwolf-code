package secretcap

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func openJar(t *testing.T, service *Service, operation string) *CookieJar {
	t.Helper()
	jar, err := service.OpenCookieJar(t.Context(), jarRequest(operation))
	testutil.FailErr(t, "open jar", err)
	return jar
}

func saveJar(t *testing.T, service *Service, jar *CookieJar) *Metadata {
	t.Helper()
	meta, err := service.SaveCookieJar(t.Context(), jar.request, jar)
	testutil.FailErr(t, "save jar", err)
	return meta
}

func jarSite(t *testing.T) *url.URL {
	t.Helper()
	site, err := url.Parse("https://example.com/")
	testutil.FailErr(t, "parse site", err)
	return site
}

func TestCookieJarConcurrentResponsesMergeIntoOneCapability(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("seeded=%v", seeded), func(t *testing.T) {
			service, _, _ := testService(t)
			site := jarSite(t)
			if seeded {
				jar := openJar(t, service, "seed")
				jar.Store.SetCookies(site, []*http.Cookie{{Name: "seed", Value: "seed-value"}})
				saveJar(t, service, jar)
			}
			const count = 8
			jars := make([]*CookieJar, count)
			for i := range jars {
				jars[i] = openJar(t, service, fmt.Sprintf("call-%d", i))
				jars[i].Store.SetCookies(site, []*http.Cookie{{Name: fmt.Sprintf("cookie%d", i), Value: "private-value"}})
			}
			start, results := make(chan struct{}), make(chan error, count)
			for _, jar := range jars {
				go func() {
					<-start
					_, err := service.SaveCookieJar(t.Context(), jar.request, jar)
					results <- err
				}()
			}
			close(start)
			for range count {
				testutil.FailErr(t, "concurrent save", <-results)
			}
			want := count
			if seeded {
				want++
			}
			loaded := openJar(t, service, "inspect")
			if got := len(loaded.Store.Snapshot()); got != want {
				t.Fatalf("merged cookies=%d, want %d", got, want)
			}
			listed, err := service.List(t.Context(), jarRequest("").ProjectID, "root-1")
			testutil.FailErr(t, "list identities", err)
			if len(listed) != 1 || listed[0].Version != int64(want) {
				t.Fatalf("jar identities = %+v", listed)
			}
			for _, jar := range jars {
				if jar.Reference != loaded.Reference {
					t.Fatal("concurrent creation minted distinct capabilities")
				}
			}
		})
	}
}

func TestCookieJarStaleResponsesNeitherLoseUpdatesNorResurrectDeletion(t *testing.T) {
	service, _, _ := testService(t)
	site := jarSite(t)
	seed := openJar(t, service, "seed")
	seed.Store.SetCookies(site, []*http.Cookie{{Name: "session", Value: "session-old"}, {Name: "keep", Value: "keep-old"}})
	saveJar(t, service, seed)
	deleteJar, stale := openJar(t, service, "delete"), openJar(t, service, "other")
	deleteJar.Store.SetCookies(site, []*http.Cookie{{Name: "session", MaxAge: -1}, {Name: "keep", Value: "keep-new"}})
	saveJar(t, service, deleteJar)
	stale.Store.SetCookies(site, []*http.Cookie{{Name: "other", Value: "other-new"}})
	saveJar(t, service, stale)
	loaded := openJar(t, service, "read")
	if _, found := loaded.Store.Lookup(site, "session"); found {
		t.Fatal("stale response resurrected deleted session")
	}
	if value, _ := loaded.Store.Lookup(site, "keep"); value != "keep-new" {
		t.Fatal("stale response overwrote an unrelated update")
	}
	if len(loaded.Store.Snapshot()) != 2 {
		t.Fatal("merge lost the response's new cookie")
	}
}

func TestCookieJarDeletingInitiallyAbsentCookieRemovesConcurrentCreation(t *testing.T) {
	service, _, _ := testService(t)
	site := jarSite(t)
	create, deletion := openJar(t, service, "create"), openJar(t, service, "delete")
	create.Store.SetCookies(site, []*http.Cookie{{Name: "sid", Value: "new-session"}})
	deletion.Store.SetCookies(site, []*http.Cookie{{Name: "sid", MaxAge: -1}})
	first := saveJar(t, service, create)
	second := saveJar(t, service, deletion)
	if second.Reference != first.Reference || second.Version != 2 {
		t.Fatalf("delete receipt = %+v", second)
	}
	if got := openJar(t, service, "read").Store.Snapshot(); len(got) != 0 {
		t.Fatal("explicit deletion was lost because its local snapshot was empty")
	}
}

func TestCookieJarResponseReplaysAreIdempotentAcrossServiceRestart(t *testing.T) {
	for _, initial := range []string{"create", "unchanged", "empty"} {
		t.Run(initial, func(t *testing.T) {
			service, values, _ := testService(t)
			site := jarSite(t)
			if initial == "unchanged" {
				seed := openJar(t, service, "seed")
				seed.Store.SetCookies(site, []*http.Cookie{{Name: "sid", Value: "original"}})
				saveJar(t, service, seed)
			}
			first := openJar(t, service, "replay")
			incoming := &http.Cookie{Name: "sid", Value: "original"}
			if initial == "empty" {
				incoming.MaxAge = -1
			}
			first.Store.SetCookies(site, []*http.Cookie{incoming})
			saveJar(t, service, first)
			later := openJar(t, service, "later")
			later.Store.SetCookies(site, []*http.Cookie{{Name: "sid", Value: "newer-value"}})
			latest := saveJar(t, service, later)
			saveJar(t, service, first)
			restarted := NewWithStore(service.handle, values, nil)
			replayed := openJar(t, restarted, "replay")
			replayed.Store.SetCookies(site, []*http.Cookie{incoming})
			saveJar(t, restarted, replayed)
			loaded := openJar(t, restarted, "read")
			if value, _ := loaded.Store.Lookup(site, "sid"); value != "newer-value" {
				t.Fatal("retry applied stale response over a later operation")
			}
			meta, err := restarted.Describe(t.Context(), jarRequest("").ProjectID, "root-1", loaded.Reference)
			testutil.FailErr(t, "describe replay", err)
			if meta.Version != latest.Version {
				t.Fatalf("retry added a version: %+v", meta)
			}
		})
	}
}

func TestCookieJarVersionAndReceiptRollbackTogether(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprint(seeded), func(t *testing.T) {
			service, values, remembered := testService(t)
			site := jarSite(t)
			if seeded {
				seed := openJar(t, service, "seed")
				seed.Store.SetCookies(site, []*http.Cookie{{Name: "sid", Value: "original"}})
				saveJar(t, service, seed)
			}
			before := len(values.IDs())
			_, err := service.handle.ExecContext(t.Context(), `CREATE TRIGGER fail_cookie_save BEFORE INSERT ON managed_cookie_jar_saves BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`)
			testutil.FailErr(t, "inject receipt failure", err)
			jar := openJar(t, service, "save")
			jar.Store.SetCookies(site, []*http.Cookie{{Name: "sid", Value: "new-value"}})
			if _, err := service.SaveCookieJar(t.Context(), jar.request, jar); err == nil {
				t.Fatal("receipt failure was ignored")
			}
			if len(values.IDs()) != before {
				t.Fatal("failed transaction leaked a protected value")
			}
			screened := false
			for _, item := range *remembered {
				if item.Secret == "new-value" && item.NonDisclosable && item.Reference == "" {
					screened = true
				}
			}
			if !screened {
				t.Fatal("persistence failure left a received credential unscreened")
			}
			loaded := openJar(t, service, "read")
			if value, _ := loaded.Store.Lookup(site, "sid"); value == "new-value" {
				t.Fatal("failed receipt committed its value")
			}
			_, err = service.handle.ExecContext(t.Context(), `DROP TRIGGER fail_cookie_save`)
			testutil.FailErr(t, "remove failure injection", err)
			saveJar(t, service, jar)
			if value, _ := openJar(t, service, "read-after-retry").Store.Lookup(site, "sid"); value != "new-value" {
				t.Fatal("failed operation could not be retried")
			}
		})
	}
}
