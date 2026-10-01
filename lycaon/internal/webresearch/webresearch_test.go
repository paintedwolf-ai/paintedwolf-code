package webresearch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFetchURLRejectsNonHTTP(t *testing.T) {
	_, err := FetchURL(context.Background(), FetchOptions{URL: "file:///etc/passwd"})
	if err == nil {
		t.Fatal("expected error for file URL")
	}
}

func TestFetchURLHTML(t *testing.T) {
	allowLoopbackFetch(t)
	srv := testHTTPServer(t, "text/html", `<html><head><title>Doc</title></head><body><p>Hello</p></body></html>`)
	res, err := FetchURL(context.Background(), FetchOptions{URL: srv})
	testutil.FailErr(t, "FetchURL", err)
	if res.Title != "Doc" {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Text, "Hello") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestExtractHTMLTextDecodesTitleEntities(t *testing.T) {
	title, _ := extractHTMLText(
		`<html><head><title>Tom &amp; Jerry &#x2014; Docs</title></head><body><p>Body</p></body></html>`,
		"https://example.com/docs",
	)
	if title != "Tom & Jerry — Docs" {
		t.Fatalf("title = %q", title)
	}
}

// The title and body share one parsed document identity.
func TestExtractHTMLTextTitleComesFromTheParsedDocument(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "comment before head title",
			raw:  `<html><!-- <title>Spoofed</title> --><head><title>Real</title></head><body><p>Body text here.</p></body></html>`,
		},
		{
			name: "script string before head title",
			raw:  `<html><head><script>var s = "<title>Spoofed</title>";</script><title>Real</title></head><body><p>Body text here.</p></body></html>`,
		},
		{
			name: "inline svg title in the body",
			raw:  `<html><head><title>Real</title></head><body><svg><title>Spoofed</title></svg><p>Body text here.</p></body></html>`,
		},
		{
			name: "textarea in the body",
			raw:  `<html><head><title>Real</title></head><body><textarea><title>Spoofed</title></textarea><p>Body text here.</p></body></html>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, _ := extractHTMLText(tc.raw, "https://example.com/page")
			if title != "Real" {
				t.Fatalf("title = %q, want %q — the title did not come from the parsed head", title, "Real")
			}
		})
	}
}

// Body titles do not identify the document.
func TestExtractHTMLTextIgnoresBodyOnlyTitle(t *testing.T) {
	title, _ := extractHTMLText(
		`<html><head></head><body><svg><title>Spoofed</title></svg><p>Body text here.</p></body></html>`,
		"https://example.com/page",
	)
	if title != "" {
		t.Fatalf("title = %q, want empty: the document declared none", title)
	}
}

func TestExtractHTMLTextPreservesResolvableLinks(t *testing.T) {
	_, text := extractHTMLText(
		`<html><body><article><p>Read the <a href="/guide?q=widgets#setup">detailed [setup] guide</a> for the complete procedure.</p></article></body></html>`,
		"https://example.com/docs/start",
	)
	want := `[detailed \[setup\] guide](<https://example.com/guide?q=widgets#setup>)`
	if !strings.Contains(text, want) {
		t.Fatalf("text = %q want link %q", text, want)
	}
}

func TestCredentialStoreReopen(t *testing.T) {
	dir := t.TempDir()
	store := NewCredentialStoreAt(dir+"/credential-vault.age", testCatalog(t))
	if err := store.Set("brave-search", "secret"); err != nil {
		testutil.FailErr(t, "Set", err)
	}
	got, ok := store.Get("brave-search")
	if !ok || got.Value() != "secret" {
		t.Fatalf("Get = %q ok=%v", got.Value(), ok)
	}
	if !store.Configured("brave-search") {
		t.Fatal("expected configured")
	}
	if err := store.Delete("brave-search"); err != nil {
		testutil.FailErr(t, "Delete", err)
	}
	if store.Configured("brave-search") {
		t.Fatal("expected not configured after delete")
	}
}

func TestValidID(t *testing.T) {
	store := NewCredentialStoreAt(t.TempDir()+"/credential-vault.age", testCatalog(t))
	if !store.ValidID("brave-search") {
		t.Fatal("brave-search should be valid")
	}
	if store.ValidID("unknown") {
		t.Fatal("unknown should be invalid")
	}
}

func TestCredentialStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-vault.age")
	cat := testCatalog(t)

	first := NewCredentialStoreAt(path, cat)
	testutil.FailErr(t, "Set", first.Set("brave-search", "stored-secret"))

	reopened, err := openCredentialStore(credentialstore.Slot{
		Path:      path,
		Namespace: credentialstore.NamespaceWebResearch,
		Context:   credentialContext,
	}, cat)
	testutil.FailErr(t, "reopen", err)

	got, ok := reopened.Get("brave-search")
	if !ok || got.Value() != "stored-secret" {
		t.Fatalf("reopened credential = %q, %v", got.Value(), ok)
	}
}
