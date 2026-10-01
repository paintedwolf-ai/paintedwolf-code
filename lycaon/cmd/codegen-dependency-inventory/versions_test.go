package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCargoMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		req, version string
		want         bool
	}{
		{"2", "2.11.2", true},
		{"2", "3.0.0", false},
		{"0.13", "0.13.4", true},
		{"0.13", "0.14.0", false},
		{"0.2.5", "0.2.9", true},
		{"0.2.5", "0.2.4", false},
		{"=0.27.4", "0.27.4", true},
		{"=0.27.4", "0.27.5", false},
		{"0.61.3", "0.62.0", false},
		{"~1.2", "1.2.9", true},
		{"~1.2", "1.3.0", false},
		{"1", "1.0.0-rc.1", false},
		{"*", "9.9.9", true},
	}
	for _, c := range cases {
		if got := cargoMatches(c.req, c.version); got != c.want {
			t.Errorf("cargoMatches(%q, %q) = %v, want %v", c.req, c.version, got, c.want)
		}
	}
}

func TestVersionIdentityDoesNotConfuseReleasePrefixesWithCommits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.26.8", "1.26.8", true},
		{"0.23.40", "0.23.400", false},
		{"150.0.7871.1", "150.0.7871.115", false},
		{"0123456789", "0123456789abcdef0123456789abcdef01234567", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := sameVersion(c.a, c.b); got != c.want {
			t.Errorf("sameVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for _, pin := range []string{"4098283", "67c4ece9ef", "^1.2.3", "2026-09-16T18:00:43Z"} {
		if versionLike(pin) {
			t.Errorf("non-release pin %q compares as a release", pin)
		}
	}
	for _, pin := range []string{"1.26.8", "v2.53.0-4", "1.30.0+paintedwolf.36", "154.0.8037.92"} {
		if !versionLike(pin) {
			t.Errorf("release pin %q is not comparable", pin)
		}
	}
}

func TestBehindComparesAlignedReleases(t *testing.T) {
	t.Parallel()
	r := row{
		pins: []value{{text: "2.53.0"}, {text: "3.7.1"}, {text: "f49d009"}},
		upstream: []upstreamRef{
			{key: "git"}, {key: "lfs"}, {key: "dugite"},
		},
	}
	snap := snapshot{Versions: map[string]string{"git": "2.55.0", "lfs": "3.7.1", "dugite": "2.53.0-4"}}
	got := r.behind(snap)
	if len(got) != 1 || got[0] != [2]string{"2.53.0", "2.55.0"} {
		t.Fatalf("behind = %v, want only the Git pair", got)
	}
	fork := row{pins: []value{{text: "1.30.0+paintedwolf.36"}}, upstream: []upstreamRef{{key: "og"}}}
	if pairs := fork.behind(snapshot{Versions: map[string]string{"og": "1.30.0"}}); len(pairs) != 0 {
		t.Fatalf("fork revision ahead of its base reported behind: %v", pairs)
	}
}

func TestPinReaderSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"a.sh":     "#!/usr/bin/env bash\nTOOL_VERSION=\"${TOOL_VERSION:-2.32.2}\"\nexport OTHER_VERSION=\"v1.2.3\"\n",
		"pin.go":   "package pin\n\nconst Pinned = \"150.0.1\"\n",
		"bun.lock": "{\n  \"packages\": {\n    \"yjs\": [\"yjs@13.6.32\", \"\", {},],\n    \"@a/b\": [\"@a/b@1.0.0\",],\n  },\n}\n",
		"doc.yaml": "vendors:\n  - commit: abc\n  - commit: def\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			testutil.FailErr(t, "write fixture", err)
		}
	}
	p := newPinReader(dir)
	cases := []struct {
		ref  sourceRef
		want string
	}{
		{sourceRef{Kind: "shell", File: "a.sh", Name: "TOOL_VERSION"}, "2.32.2"},
		{sourceRef{Kind: "shell", File: "a.sh", Name: "OTHER_VERSION"}, "v1.2.3"},
		{sourceRef{Kind: "go-const", File: "pin.go", Name: "Pinned"}, "150.0.1"},
		{sourceRef{Kind: "npm-lock", File: "bun.lock", Name: "@a/b"}, "1.0.0"},
		{sourceRef{Kind: "yaml", File: "doc.yaml", Key: "vendors.1.commit"}, "def"},
		{sourceRef{Kind: "yaml", File: "doc.yaml", Key: "vendors", Count: true}, "2"},
	}
	for _, c := range cases {
		got, err := p.resolve(c.ref)
		if err != nil {
			testutil.FailErr(t, "resolve "+c.ref.Kind, err)
		}
		if got != c.want {
			t.Errorf("resolve(%+v) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestParseSource(t *testing.T) {
	t.Parallel()
	got, err := parseSource(`github-tag git/git ^v\d+$`)
	if err != nil {
		testutil.FailErr(t, "parse github-tag", err)
	}
	if got.Kind != "github-tag" || got.Repo != "git/git" || got.Pattern != `^v\d+$` || got.Strip != "" {
		t.Fatalf("github-tag parsed as %+v", got)
	}
	count, err := parseSource("yaml-count rules.yaml vendors")
	if err != nil {
		testutil.FailErr(t, "parse yaml-count", err)
	}
	if count.Kind != "yaml" || !count.Count || count.Key != "vendors" {
		t.Fatalf("yaml-count parsed as %+v", count)
	}
	for _, bad := range []string{"", "shell only-a-path", "rust-stable extra", "nope x"} {
		if _, err := parseSource(bad); err == nil {
			t.Errorf("parseSource(%q) accepted a malformed source", bad)
		}
	}
}
