package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
)

func TestCandidateRejectsExecutableAndDigestOverrides(t *testing.T) {
	for _, args := range [][]string{{"-executable", "/other/opengrep"}, {"-sha256", "other-digest"}} {
		t.Run(args[0], func(t *testing.T) {
			previousFlags, previousArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = previousFlags, previousArgs })
			flag.CommandLine = flag.NewFlagSet("opengrep-artifact", flag.ContinueOnError)
			os.Args = append([]string{"opengrep-artifact"}, args...)
			t.Setenv(bundled.EnvOpenGrepCandidate, t.TempDir())
			if _, err := run(); err == nil {
				t.Fatal("candidate combined with an independent executable identity")
			}
		})
	}
}

func TestArtifactModesKeepBuildAndRuntimeAuthoritySeparate(t *testing.T) {
	t.Setenv(bundled.EnvOpenGrepCandidate, "")
	for name, opts := range map[string]options{
		"identity missing directory":    {mode: "identity"},
		"identity with stage root":      {mode: "identity", directory: "/artifact", root: "/stage"},
		"verify self declared identity": {mode: "verify", root: "/stage", directory: "/artifact"},
		"stage explicit executable":     {mode: "stage", executable: "/other", digest: "hash"},
		"digest without executable":     {mode: "resolve", root: "/stage", digest: "hash"},
		"unknown mode":                  {mode: "download", root: "/stage"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := opts.validate(); err == nil {
				t.Fatal("ambiguous artifact authority accepted")
			}
		})
	}
}

func TestReleaseCommandsRequireExplicitAuthority(t *testing.T) {
	t.Setenv(bundled.EnvOpenGrepCandidate, "")
	for name, opts := range map[string]options{
		"fetch":          {mode: "fetch", cacheRoot: "/cache"},
		"offline import": {mode: "fetch", cacheRoot: "/cache", archive: "/release.tar.gz", offline: true},
		"select":         {mode: "select", cacheRoot: "/cache", releaseManifest: "https://example.invalid/release.json", manifestOutput: "/manifest.yaml", tag: "v1.30.0+paintedwolf.33", expectedCommit: strings.Repeat("a", 40)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := opts.validate(); err != nil {
				t.Fatalf("valid release command: %v", err)
			}
		})
	}
	for name, opts := range map[string]options{
		"select no expected commit":       {mode: "select", cacheRoot: "/cache", releaseManifest: "/descriptor", manifestOutput: "/manifest", tag: "v1.30.0+paintedwolf.33"},
		"select no tag":                   {mode: "select", cacheRoot: "/cache", releaseManifest: "/descriptor", manifestOutput: "/manifest", expectedCommit: strings.Repeat("a", 40)},
		"offline authenticated selection": {mode: "select", cacheRoot: "/cache", releaseManifest: "/descriptor", manifestOutput: "/manifest", tag: "v1.30.0+paintedwolf.33", expectedCommit: strings.Repeat("a", 40), offline: true},
		"no cache":                        {mode: "fetch"},
		"fetch override":                  {mode: "fetch", cacheRoot: "/cache", directory: "/artifact"},
		"fetch descriptor":                {mode: "fetch", cacheRoot: "/cache", releaseManifest: "/descriptor"},
		"select no descriptor":            {mode: "select", cacheRoot: "/cache", manifestOutput: "/manifest"},
		"select no output":                {mode: "select", cacheRoot: "/cache", releaseManifest: "/descriptor"},
		"select cross platform":           {mode: "select", cacheRoot: "/cache", releaseManifest: "/descriptor", manifestOutput: "/manifest", target: "aarch64-apple-darwin"},
		"offline remote descriptor":       {mode: "select", cacheRoot: "/cache", releaseManifest: "https://example.invalid/descriptor", manifestOutput: "/manifest", offline: true},
		"stage release options":           {mode: "stage", root: "/stage", cacheRoot: "/cache"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := opts.validate(); err == nil {
				t.Fatal("ambiguous release command accepted")
			}
		})
	}
	t.Setenv(bundled.EnvOpenGrepCandidate, "/candidate")
	if err := (options{mode: "fetch", cacheRoot: "/cache"}).validate(); err == nil {
		t.Fatal("candidate combined with pinned release fetch")
	}
}
