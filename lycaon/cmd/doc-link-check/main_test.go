package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// Docs anchor a paragraph that is not its own heading with an HTML anchor, and
// other docs link to it. Both spellings have to register.
func TestFileAnchorsAcceptsHeadingsAndHTMLAnchors(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "sample.md", `# Title

## Detection packs

<a id="overlay-not-floor"></a>
**Overlay, not floor.** Prose that carries its own anchor.

<a name="legacy-spelling"></a>

`+"```"+`
<a id="inside-a-fence"></a>
## Heading inside a fence
`+"```"+`
`)

	got := fileAnchors(path)

	for _, want := range []string{"title", "detection-packs", "overlay-not-floor", "legacy-spelling"} {
		if !got[want] {
			t.Errorf("anchor %q not registered; have %v", want, got)
		}
	}
	for _, absent := range []string{"inside-a-fence", "heading-inside-a-fence"} {
		if got[absent] {
			t.Errorf("anchor %q inside a fence must not register", absent)
		}
	}
}

// A link to an anchor the target does not declare fails the check.
func TestCheckFileReportsAMissingAnchor(t *testing.T) {
	dir := t.TempDir()
	target := writeDoc(t, dir, "target.md", "# Real heading\n\n<a id=\"real-anchor\"></a>\nBody.\n")
	source := writeDoc(t, dir, "source.md", `# Source

[good heading](target.md#real-heading)
[good anchor](target.md#real-anchor)
[bad](target.md#no-such-anchor)
`)

	anchors := map[string]map[string]bool{
		target: fileAnchors(target),
		source: fileAnchors(source),
	}
	problems := checkFile(source, anchors)

	if len(problems) != 1 {
		t.Fatalf("want exactly the one bad anchor, got %v", problems)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("test setup expects absolute paths, got %q", target)
	}
}
