package mcp

import (
	"strings"
	"testing"
)

func TestValidateRecipeShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		recipe  Recipe
		wantSub string
	}{
		{
			name:    "both transports",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Command: "npx", Auth: RecipeAuthOAuth},
			wantSub: "both url and command",
		},
		{
			name:    "oauth without url",
			recipe:  Recipe{Label: "Bad", Hint: "hint", Command: "npx", Auth: RecipeAuthOAuth},
			wantSub: "requires url",
		},
		{
			name:    "static bearer without label",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Auth: RecipeAuthStaticToken},
			wantSub: "credential_label",
		},
		{
			name:    "unknown auth",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Auth: "pat"},
			wantSub: "invalid auth",
		},
		{
			name:    "non-canonical url",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "example.com/mcp", Auth: RecipeAuthOAuth},
			wantSub: "canonical",
		},
		{
			name:    "unknown credential wire",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Auth: RecipeAuthOAuth, CredentialWire: "pat"},
			wantSub: "credential_wire",
		},
		{
			name:    "header wire without name",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Auth: RecipeAuthStaticToken, CredentialLabel: "Key", CredentialWire: CredentialWireHeader},
			wantSub: "credential_header",
		},
		{
			name:    "header name without header wire",
			recipe:  Recipe{Label: "Bad", Hint: "hint", URL: "https://example.com/mcp", Auth: RecipeAuthStaticToken, CredentialLabel: "Key", CredentialHeader: "X-Api-Key"},
			wantSub: "credential_header requires",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateRecipe("bad", tc.recipe)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v want %q", err, tc.wantSub)
			}
		})
	}
}

func TestRecipeProjectOK(t *testing.T) {
	t.Parallel()
	loop := Recipe{Auth: RecipeAuthNone, URL: "http://127.0.0.1:8765/mcp"}
	if !loop.ProjectOK() {
		t.Fatal("loopback none must be project-eligible")
	}
	remote := Recipe{Auth: RecipeAuthNone, URL: "https://example.com/mcp"}
	if remote.ProjectOK() {
		t.Fatal("remote none must not be project-eligible")
	}
	token := Recipe{Auth: RecipeAuthStaticToken, URL: "http://127.0.0.1:8765/mcp"}
	if token.ProjectOK() {
		t.Fatal("credential-bearing recipe must not be project-eligible")
	}
}
