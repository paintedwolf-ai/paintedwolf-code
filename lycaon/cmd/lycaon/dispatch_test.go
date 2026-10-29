package main

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

func TestClassifyArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		args     []string
		wantKind dispatchKind
		wantVerb string
		wantRest []string
	}{
		{name: "empty opens cwd", args: nil, wantKind: dispatchOpen, wantVerb: "open", wantRest: nil},
		{name: "dot opens", args: []string{"."}, wantKind: dispatchOpen, wantVerb: "open", wantRest: []string{"."}},
		{name: "path opens", args: []string{"/tmp/proj"}, wantKind: dispatchOpen, wantVerb: "open", wantRest: []string{"/tmp/proj"}},
		{name: "new flag opens", args: []string{"--new", "."}, wantKind: dispatchOpen, wantVerb: "open", wantRest: []string{"--new", "."}},
		{name: "project name opens", args: []string{"MyProject"}, wantKind: dispatchOpen, wantVerb: "open", wantRest: []string{"MyProject"}},
		{name: "open synonym", args: []string{"open", "."}, wantKind: dispatchVerb, wantVerb: "open", wantRest: []string{"."}},
		{name: "ls verb", args: []string{"ls"}, wantKind: dispatchVerb, wantVerb: "ls", wantRest: []string{}},
		{name: "logs verb", args: []string{"logs", "--follow"}, wantKind: dispatchVerb, wantVerb: "logs", wantRest: []string{"--follow"}},
		{name: "extensions verb", args: []string{"extensions", "validate"}, wantKind: dispatchVerb, wantVerb: "extensions", wantRest: []string{"validate"}},
		{name: "native proxy helper verb", args: []string{"internal-system-proxy"}, wantKind: dispatchVerb, wantVerb: "internal-system-proxy", wantRest: []string{}},
		{name: "version flag", args: []string{"--version"}, wantKind: dispatchVersion, wantVerb: "--version", wantRest: []string{}},
		{name: "project named version opens", args: []string{"version"}, wantKind: dispatchOpen, wantVerb: "open", wantRest: []string{"version"}},
		{name: "internal scan worker verb", args: []string{"internal-scan-worker"}, wantKind: dispatchVerb, wantVerb: "internal-scan-worker", wantRest: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind, verb, rest := classifyArgs(tc.args)
			if rest == nil {
				rest = []string{}
			}
			wantRest := tc.wantRest
			if wantRest == nil {
				wantRest = []string{}
			}
			if kind != tc.wantKind || verb != tc.wantVerb || !reflect.DeepEqual(rest, wantRest) {
				t.Fatalf("classifyArgs(%q) = (%v, %q, %q), want (%v, %q, %q)",
					tc.args, kind, verb, rest, tc.wantKind, tc.wantVerb, wantRest)
			}
		})
	}
}

func TestIsCLIVerb(t *testing.T) {
	t.Parallel()
	if !isCLIVerb("ls") || !isCLIVerb("open") || isCLIVerb(".") || isCLIVerb("MyProject") {
		t.Fatal("reserved verbs / open argv classification mismatch")
	}
}

func TestVersionFlagPrintsTheStampedProductVersion(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := printVersion(&out, nil); err != nil {
		testutil.FailErr(t, "printVersion failed", err)
	}
	if want := "Painted Wolf Code " + version.Version + "\n"; out.String() != want {
		t.Fatalf("printVersion = %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := printVersion(&out, []string{"extra"}); err == nil || out.Len() != 0 {
		t.Fatalf("--version with arguments = %q, %v", out.String(), err)
	}
}
