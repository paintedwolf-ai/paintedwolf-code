package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSuiteMembershipHasItsOwnVersionAndAPIRequirement(t *testing.T) {
	const file = packsRoot + "/painted-wolf/meta.yaml"
	raw := []byte("id: painted-wolf/stock\nversion: 1.0.0\ncompatibility:\n  extension_api: '^1.0.0'\nmembers: [painted-wolf/one]\n")
	before := treeRelease{file: raw}
	for _, tc := range []struct{ name, old, next, want string }{
		{name: "unchanged"},
		{name: "changed membership", old: "one", next: "two", want: "without a version bump"},
		{name: "version advances", old: "version: 1.0.0", next: "version: 1.0.1"},
		{name: "version regresses", old: "version: 1.0.0", next: "version: 0.9.0", want: "regressed"},
		{name: "API unavailable", old: "^1.0.0", next: "^2.0.0", want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := treeRelease{file: bytes.ReplaceAll(raw, []byte(tc.old), []byte(tc.next)), "lycaon/internal/extpacks/manifest.go": []byte(`package extpacks; const ExtensionAPIVersion="1.1.0"`)}
			if tc.name == "API unavailable" {
				current[file] = bytes.ReplaceAll(current[file], []byte("version: 1.0.0"), []byte("version: 1.0.1"))
			}
			err := checkReleasedSuite(before, current, file)
			if tc.want == "" {
				testutil.FailErr(t, "check suite", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("suite=%v, want %s", err, tc.want)
			}
		})
	}
}

func TestAnchorContractChangesAdvanceTheirInterpretationVersion(t *testing.T) {
	const file = packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml"
	raw := "schema_version: '2.0'\nanchors:\n - id: gate.blocked\n   surface: coordinator\n   planes: [inform]\n   envelope:\n     vars:\n       detail: {type: string}\n"
	before := treeRelease{file: []byte(raw)}
	for _, tc := range []struct{ name, version, old, next, want string }{
		{name: "unchanged", version: "2.0"},
		{name: "display copy", version: "2.0", old: "   surface:", next: "   description: changed copy\n   surface:"},
		{name: "type change", version: "2.1", old: "type: string", next: "type: bool", want: "major"},
		{name: "required field", version: "2.1", old: "type: string", next: "type: string, required: true", want: "major"},
		{name: "breaking epoch", version: "3.0", old: "type: string", next: "type: bool"},
		{name: "new anchor without version", version: "2.0", old: "anchors:\n", next: "anchors:\n - id: added\n", want: "advance"},
		{name: "new anchor with minor", version: "2.1", old: "anchors:\n", next: "anchors:\n - id: added\n"},
		{name: "downgrade", version: "1.9", want: "regressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := strings.Replace(raw, "'2.0'", "'"+tc.version+"'", 1)
			if tc.old != "" {
				next = strings.Replace(next, tc.old, tc.next, 1)
			}
			err := checkAnchorVersions(before, treeRelease{file: []byte(next)})
			if tc.want == "" {
				testutil.FailErr(t, "check anchor epoch", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("anchor=%v, want %s", err, tc.want)
			}
		})
	}
}
