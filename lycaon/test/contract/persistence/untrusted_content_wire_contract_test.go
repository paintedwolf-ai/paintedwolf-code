package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestUntrustedContentWireFieldPresentAndReadOnly(t *testing.T) {
	t.Parallel()
	assertJSONField(t, reflect.TypeOf(api.Session{}), "UntrustedContent", "untrusted_content")
	assertJSONField(t, reflect.TypeOf(api.SessionEvent{}), "UntrustedContent", "untrusted_content")
	assertNoJSONField(t, reflect.TypeOf(api.CreateSessionRequest{}), "UntrustedContent", "untrusted_content")
	assertNoJSONField(t, reflect.TypeOf(api.PromptRequest{}), "UntrustedContent", "untrusted_content")
	assertNoJSONField(t, reflect.TypeOf(api.AbortSessionRequest{}), "UntrustedContent", "untrusted_content")

	root := contractcheck.RepoRoot(t)
	sessionSchemas := filepath.Join(root, "docs", "openapi", "components", "schemas", "session")
	readSchemas := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(sessionSchemas, name))
		if err != nil {
			t.Fatalf("read session/%s: %v", name, err)
		}
		return string(body)
	}
	identity := readSchemas("identity.yaml")

	sessionBlock := openAPISchemaBlock(t, identity, "Session")
	if !strings.Contains(sessionBlock, "untrusted_content:") {
		t.Fatal("Session OpenAPI schema missing untrusted_content")
	}
	createBlock := openAPISchemaBlock(t, identity, "CreateSessionRequest")
	if strings.Contains(createBlock, "untrusted_content:") {
		t.Fatal("CreateSessionRequest must not accept untrusted_content (read-only)")
	}
	eventBlock := openAPISchemaBlock(t, readSchemas("events.yaml"), "SessionEvent")
	if !strings.Contains(eventBlock, "untrusted_content:") {
		t.Fatal("SessionEvent OpenAPI schema missing untrusted_content")
	}
}

func openAPISchemaBlock(t *testing.T, text, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^    ` + regexp.QuoteMeta(name) + `:\n`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		t.Fatalf("missing %s schema", name)
	}
	rest := text[loc[0]:]
	next := regexp.MustCompile(`(?m)^    [A-Za-z]`).FindStringIndex(rest[1:])
	if next == nil {
		return rest
	}
	return rest[:next[0]+1]
}
