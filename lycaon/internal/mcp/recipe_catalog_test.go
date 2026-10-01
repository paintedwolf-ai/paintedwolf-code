package mcp_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
)

func TestLoadRecipeCatalog(t *testing.T) {
	cat, err := mcp.LoadRecipeCatalog()
	if err != nil {
		t.Fatalf("LoadRecipeCatalog: %v", err)
	}
	github, ok := cat.Entry("github")
	if !ok {
		t.Fatal("github recipe missing")
	}
	if github.Auth != mcp.RecipeAuthStaticToken {
		t.Fatalf("github auth = %q", github.Auth)
	}
	if github.URL != "https://api.githubcopilot.com/mcp" {
		t.Fatalf("github url = %q", github.URL)
	}
	if github.Class() != mcp.ProviderClassWeb {
		t.Fatalf("github class = %q", github.Class())
	}
	if github.ProjectOK() {
		t.Fatal("remote recipe must not be project-eligible")
	}
	hf, ok := cat.Entry("huggingface")
	if !ok || hf.Auth != mcp.RecipeAuthStaticToken {
		t.Fatalf("huggingface = %+v ok=%v", hf, ok)
	}
	gitlab, ok := cat.Entry("gitlab")
	if !ok || gitlab.Auth != mcp.RecipeAuthOAuth || gitlab.ProjectOK() {
		t.Fatalf("gitlab = %+v ok=%v", gitlab, ok)
	}

	atlassian, ok := cat.Entry("atlassian")
	if !ok || atlassian.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
		t.Fatalf("atlassian = %+v ok=%v", atlassian, ok)
	}
	context7, ok := cat.Entry("context7")
	if !ok || context7.CredentialWire != mcp.CredentialWireHeader || context7.CredentialHeader != "CONTEXT7_API_KEY" {
		t.Fatalf("context7 = %+v ok=%v", context7, ok)
	}
	firecrawl, ok := cat.Entry("firecrawl")
	if !ok || firecrawl.Auth != mcp.RecipeAuthOAuth || firecrawl.URL != "https://mcp.firecrawl.dev/v2/mcp-oauth" {
		t.Fatalf("firecrawl = %+v ok=%v", firecrawl, ok)
	}

	playwright, ok := cat.Entry("playwright")
	if !ok || playwright.Auth != mcp.RecipeAuthNone || playwright.Class() != mcp.ProviderClassLocal {
		t.Fatalf("playwright = %+v ok=%v", playwright, ok)
	}
	supabaseLocal, ok := cat.Entry("supabase-local")
	if !ok || !supabaseLocal.ProjectOK() || supabaseLocal.Class() != mcp.ProviderClassLocal {
		t.Fatalf("supabase-local = %+v ok=%v", supabaseLocal, ok)
	}
	githubLocal, ok := cat.Entry("github-local")
	if !ok || len(githubLocal.EnvKeys) != 2 || githubLocal.EnvKeys[0].Key != "GITHUB_PERSONAL_ACCESS_TOKEN" || githubLocal.EnvKeys[1].Key != "GITHUB_HOST" {
		t.Fatalf("github-local = %+v ok=%v", githubLocal, ok)
	}

	ov, ok := cat.OverlayFor("github")
	if !ok {
		t.Fatal("OverlayFor github")
	}
	if ov.Enabled == nil || *ov.Enabled {
		t.Fatal("recipe overlay must start disabled")
	}
	if ov.Recipe == nil || *ov.Recipe != "github" {
		t.Fatalf("recipe = %v", ov.Recipe)
	}
	if ov.URL == nil || *ov.URL != github.URL {
		t.Fatalf("overlay url = %v", ov.URL)
	}

	pd, ok := cat.Entry("pagerduty")
	if !ok || pd.Auth != mcp.RecipeAuthStaticToken || pd.CredentialWire != mcp.CredentialWireTokenToken {
		t.Fatalf("pagerduty = %+v ok=%v", pd, ok)
	}
	if pd.URL != "https://mcp.pagerduty.com/mcp" {
		t.Fatalf("pagerduty url = %q", pd.URL)
	}

	want := []string{
		"playwright", "chrome-devtools", "github-local", "supabase-local",
		"circleci-cli", "sequential-thinking",
		"github", "gitlab", "notion", "linear", "atlassian",
		"supabase", "neon", "sentry", "posthog", "circleci",
		"stripe", "huggingface", "context7", "firecrawl", "pagerduty",
	}
	got := make([]string, 0, len(cat.Entries()))
	var sawOAuth, sawStatic, sawNone, sawLocal, sawWeb bool
	for _, rec := range cat.Entries() {
		if rec.ID == "" || rec.Label == "" || rec.Hint == "" {
			t.Fatalf("incomplete recipe %+v", rec)
		}
		got = append(got, rec.ID)
		switch rec.Auth {
		case mcp.RecipeAuthOAuth:
			sawOAuth = true
		case mcp.RecipeAuthStaticToken:
			sawStatic = true
		case mcp.RecipeAuthNone:
			sawNone = true
		}
		switch rec.Class() {
		case mcp.ProviderClassLocal:
			sawLocal = true
		case mcp.ProviderClassWeb:
			sawWeb = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("recipes = %v want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("recipes[%d] = %q want %q (full %v)", i, got[i], id, got)
		}
	}
	if !sawOAuth || !sawStatic || !sawNone {
		t.Fatal("catalog must include oauth, static_token, and none recipes")
	}
	if !sawLocal || !sawWeb {
		t.Fatal("catalog must include local and web recipes")
	}
}

func TestRecipeCatalogUnknown(t *testing.T) {
	cat, err := mcp.LoadRecipeCatalog()
	if err != nil {
		t.Fatalf("LoadRecipeCatalog: %v", err)
	}
	if _, ok := cat.Entry("not-a-recipe"); ok {
		t.Fatal("unknown id must miss")
	}
	if _, ok := cat.OverlayFor(""); ok {
		t.Fatal("empty id must miss")
	}
}
