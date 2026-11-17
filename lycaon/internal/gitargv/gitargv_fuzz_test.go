package gitargv_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitargv"
)

// Git runs git-remote-<name> for <name>::<address> before it reads anything else.
var gitHelper = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)::`)

// gitReadsLocal is how Git reads a clone source: a helper is remote; any
// "://" makes a URL, local only as file:// with a path; otherwise a colon
// before any slash is scp-like.
func gitReadsLocal(source string) bool {
	if gitHelper.MatchString(source) {
		return false
	}
	if strings.Contains(source, "://") {
		rest, file := strings.CutPrefix(source, "file://")
		return file && strings.Contains(rest, "/")
	}
	colon, slash := strings.IndexByte(source, ':'), strings.IndexByte(source, '/')
	return colon < 0 || slash >= 0 && slash < colon
}

// FuzzGitArgvWordsStayOperands requires that no accepted ref or source
// reaches Git as an option or a command-running helper, that a source is
// local exactly when Git reads it as a path, and that a path read from a
// file:// URL reads back the same once rendered as one.
func FuzzGitArgvWordsStayOperands(f *testing.F) {
	for _, seed := range []string{
		"main", "-b", " --upload-pack=sh", "\u00a0-x", "v1.2.3", "ext::sh -c id", "EXT::sh", "fd::7",
		"https://example.com/r.git", "git@example.com:r.git", "/srv/r.git", "./r", "r", "x/y://z",
		"file:///srv/r.git", "file://localhost/srv/r.git", "FILE:///srv/r.git", "file://host", "file:///a%2Fb%20c",
		"a::b", "a/b::c", "C:/repo", "--end-of-options",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, word string) {
		if gitargv.ValidateRefArg(word) == nil && strings.HasPrefix(strings.TrimSpace(word), "-") {
			t.Fatalf("ValidateRefArg admitted option %q", word)
		}
		if gitargv.ValidateCloneURL(word) == nil {
			trimmed := strings.TrimSpace(word)
			if strings.HasPrefix(trimmed, "-") {
				t.Fatalf("ValidateCloneURL admitted option %q", word)
			}
			if helper := gitHelper.FindStringSubmatch(trimmed); helper != nil && (strings.EqualFold(helper[1], "ext") || strings.EqualFold(helper[1], "fd")) {
				t.Fatalf("ValidateCloneURL admitted command helper %q", word)
			}
		}
		path, local := gitargv.LocalCloneSourcePath(word)
		trimmed := strings.TrimSpace(word)
		if want := trimmed != "" && !strings.HasPrefix(trimmed, "-") && gitReadsLocal(trimmed); local != want {
			t.Fatalf("LocalCloneSourcePath(%q) local=%v; Git reads it local=%v", word, local, want)
		}
		if !local || !strings.HasPrefix(trimmed, "file://") {
			return
		}
		rendered := "file://" + (&url.URL{Path: path}).EscapedPath()
		if again, ok := gitargv.LocalCloneSourcePath(rendered); !ok || again != path {
			t.Fatalf("LocalCloneSourcePath(%q) = %q, rendered %q reads back %q %v", word, path, rendered, again, ok)
		}
	})
}
