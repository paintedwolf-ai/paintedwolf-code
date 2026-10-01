package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/clientnotice"
)

func TestRenderTypeScriptNormalizesCatalogEntries(t *testing.T) {
	cat := &clientnotice.Catalog{Notices: []clientnotice.Notice{
		{Kind: "fetch_failure", Scope: clientnotice.ScopeApp, Title: "Fetch failed", Message: "Try again."},
		{Kind: "update_available", Scope: clientnotice.ScopeApp, Title: "Update ready", Message: "Restart.", Action: "retry_contribution_frame"},
	}}

	got := renderTypeScript(cat)
	for _, want := range []string{
		"export type ClientNoticeKind =\n  | \"fetch_failure\"\n  | \"update_available\";",
		"export const CLIENT_NOTICES: Readonly<Record<ClientNoticeKind, ClientNoticeCopy>> = {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated TypeScript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "satisfies Record<string") {
		t.Fatalf("generated TypeScript uses an open catalog key type:\n%s", got)
	}
}
