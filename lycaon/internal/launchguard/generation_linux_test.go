//go:build linux

package launchguard

import (
	"strings"
	"testing"
)

func TestGenerationPolicyRejectsUnboundSelection(t *testing.T) {
	valid := `{"version":1,"generation_root":"/opt/painted-wolf-code/generations/abc","manifest_sha256":"` + strings.Repeat("a", 64) + `","logical_install_root":"/opt/painted-wolf-code"}`
	if _, err := decodePolicy([]byte(valid)); err != nil {
		t.Fatalf("decode bound generation: %v", err)
	}
	cases := map[string]string{
		"unknown field":       strings.TrimSuffix(valid, "}") + `,"command":"/bin/sh"}`,
		"trailing document":   valid + `{}`,
		"relative generation": strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"relative"`, 1),
		"unclean generation":  strings.Replace(valid, `generations/abc`, `generations/../abc`, 1),
		"root generation":     strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"/"`, 1),
		"uppercase hash":      strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"short hash":          strings.Replace(valid, strings.Repeat("a", 64), "ab", 1),
		"future version":      strings.Replace(valid, `"version":1`, `"version":2`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePolicy([]byte(raw)); err == nil {
				t.Fatal("accepted unbound generation")
			}
		})
	}
}

func TestTrustedPathRejectsRelativeAndWritableAncestors(t *testing.T) {
	if file, err := openRootControlled("relative", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted relative authority")
	}
	// A caller-owned temporary directory is deliberately not an installation
	// authority, regardless of whether its eventual file exists.
	if file, err := openRootControlled(t.TempDir()+"/missing", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted caller-owned authority")
	}
}
