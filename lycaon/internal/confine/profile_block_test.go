package confine_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Block-scoped assertions isolate rule precedence.

// Rule order distinguishes the read allow-back block.
const (
	blockAllowWrite    = "(allow file-write*"
	blockDenyWrite     = "(deny file-write*"
	blockDenyRead      = "(deny file-read*"
	blockAllowReadBack = "(allow file-read*"
)

// profileBlock returns the first matching rule block at or after from.
func profileBlock(t *testing.T, profile, opener string, from int) (body string, at int) {
	t.Helper()
	idx := strings.Index(profile[from:], opener)
	if idx < 0 {
		t.Fatalf("profile has no %s block:\n%s", opener, profile)
	}
	at = from + idx
	rest := profile[at:]
	if end := strings.Index(rest, ")\n("); end >= 0 {
		return rest[:end], at
	}
	return rest, at
}

func profileBlockContaining(t *testing.T, profile, opener, needle string) (body string, at int) {
	t.Helper()
	for from := 0; from < len(profile); {
		idx := strings.Index(profile[from:], opener)
		if idx < 0 {
			break
		}
		body, at = profileBlock(t, profile, opener, from)
		if strings.Contains(body, needle) {
			return body, at
		}
		from = at + len(body)
	}
	t.Fatalf("profile has no %s block containing %q:\n%s", opener, needle, profile)
	return "", -1
}

// readAllowBackBlock returns the re-allow block after the secret read deny.
func readAllowBackBlock(t *testing.T, profile string) string {
	t.Helper()
	_, denyAt := profileBlock(t, profile, blockDenyRead, 0)
	body, _ := profileBlock(t, profile, blockAllowReadBack, denyAt)
	return body
}

// assertBlockCoversPath requires a resolved subpath rule.
func assertBlockCoversPath(t *testing.T, block, path, what string) {
	t.Helper()
	if !strings.Contains(block, `(subpath "`+resolvedForProfile(path)+`")`) {
		t.Fatalf("%s must cover %q, block was:\n%s", what, path, block)
	}
}

// assertBlockOmitsPath requires the block to omit path.
func assertBlockOmitsPath(t *testing.T, block, path, what string) {
	t.Helper()
	if strings.Contains(block, `(subpath "`+resolvedForProfile(path)+`")`) {
		t.Fatalf("%s must not cover %q, block was:\n%s", what, path, block)
	}
}

// assertBlockMentions requires an exact rendered fragment.
func assertBlockMentions(t *testing.T, block, needle, what string) {
	t.Helper()
	if !strings.Contains(block, needle) {
		t.Fatalf("%s must mention %q, block was:\n%s", what, needle, block)
	}
}

func resolvedForProfile(path string) string {
	return fspath.CanonicalPath(path)
}
