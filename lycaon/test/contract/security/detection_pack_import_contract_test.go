package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func writeContractMinimalPack(t *testing.T, dir, id, ruleBody string) {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(
		"id: "+id+"\nlabel: L\ndescription: D\n",
	), 0o600))
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "hit.yml"), []byte(ruleBody), 0o600))
}

func contractGoodRule(title string) string {
	return `title: ` + title + `
id: 33333333-3333-4333-8333-333333333331
description: consequence
logsource:
  product: lycaon
  service: tool_exec
level: high
detection:
  sel:
    Image: demobin
  condition: sel
`
}

func contractCriticalMatchAllRule() string {
	return `title: Everything
id: 77777777-7777-4777-8777-777777777777
description: matches any command
logsource:
  product: lycaon
  service: tool_exec
level: critical
detection:
  sel:
    CommandLine|contains: ' '
  condition: sel
`
}

// packTreeFiles lists every file under home, split by whether it lies under
// the device packs directory.
func packTreeFiles(t *testing.T, home, configDir string) (inside, outside []string) {
	t.Helper()
	deviceRoot := detectionpack.DevicePacksDir(configDir)
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(path, deviceRoot+string(os.PathSeparator)) {
			inside = append(inside, filepath.ToSlash(rel))
		} else {
			outside = append(outside, filepath.ToSlash(rel))
		}
		return nil
	})
	testutil.FailErr(t, "walk import home", err)
	return inside, outside
}

// TestDetectionPackImportCannotEscape: hostile sources write nothing outside
// the device packs directory, and a benign import into the same home lands
// there, so an empty walk cannot satisfy the check.
func TestDetectionPackImportCannotEscape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		folder   string
		wantPack string
		prep     func(t *testing.T, src string)
	}{
		{
			name: "traversal id",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "ok-pack", contractGoodRule("Hit"))
				testutil.FailErr(t, "hostile id", os.WriteFile(filepath.Join(src, "pack.yaml"), []byte(
					"id: ../escape\nlabel: L\ndescription: D\n",
				), 0o600))
			},
		},
		{
			name:     "rules symlink to etc",
			wantPack: "sym-escape",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "sym-escape", contractGoodRule("Hit"))
				testutil.FailErr(t, "symlink", os.Symlink("/etc/passwd", filepath.Join(src, "rules", "etc.yml")))
			},
		},
		{
			name:     "dotdot folder name",
			folder:   "..dotdot-named",
			wantPack: "dotdot-pack",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "dotdot-pack", contractGoodRule("Hit"))
			},
		},
		{
			name:     "deeply nested tree",
			wantPack: "deep-pack",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "deep-pack", contractGoodRule("Hit"))
				deep := filepath.Join(src, "a", "b", "c", "d")
				testutil.FailErr(t, "mkdir deep", os.MkdirAll(deep, 0o700))
				testutil.FailErr(t, "nested", os.WriteFile(filepath.Join(deep, "x.yml"), []byte("x"), 0o600))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cfg := filepath.Join(home, "config")
			testutil.FailErr(t, "mkdir config", os.MkdirAll(cfg, 0o700))
			folder := tc.folder
			if folder == "" {
				folder = "src"
			}
			src := filepath.Join(t.TempDir(), folder)
			tc.prep(t, src)
			_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
			if (err == nil) != (tc.wantPack != "") {
				t.Fatalf("hostile import err = %v, want a committed pack %q", err, tc.wantPack)
			}
			inside, outside := packTreeFiles(t, home, cfg)
			if len(outside) > 0 {
				t.Fatalf("wrote outside device packs: %v", outside)
			}
			if tc.wantPack != "" && !slices.Contains(inside, "config/detection-packs/"+tc.wantPack+"/pack.yaml") {
				t.Fatalf("committed pack %q missing under device packs: %v", tc.wantPack, inside)
			}

			control := filepath.Join(t.TempDir(), "control")
			writeContractMinimalPack(t, control, "control-pack", contractGoodRule("Hit"))
			_, err = detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: control})
			testutil.FailErr(t, "benign import", err)
			inside, outside = packTreeFiles(t, home, cfg)
			if len(outside) > 0 {
				t.Fatalf("benign import wrote outside device packs: %v", outside)
			}
			if !slices.Contains(inside, "config/detection-packs/control-pack/pack.yaml") {
				t.Fatalf("benign pack missing under device packs: %v", inside)
			}
		})
	}
}

// TestDetectionPackImportAllowlistHonored: only pack.yaml, rules/*.yml, fixtures.yaml copy.
func TestDetectionPackImportAllowlistHonored(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "allow-pack", contractGoodRule("Hit"))
	testutil.FailErr(t, "fixtures", os.WriteFile(filepath.Join(src, "fixtures.yaml"), []byte("fixtures: []\n"), 0o600))
	testutil.FailErr(t, "evil.sh", os.WriteFile(filepath.Join(src, "evil.sh"), []byte("#!/bin/sh\n"), 0o600))
	testutil.FailErr(t, ".git", os.MkdirAll(filepath.Join(src, ".git"), 0o700))
	testutil.FailErr(t, "node_modules", os.MkdirAll(filepath.Join(src, "node_modules", "x"), 0o700))
	testutil.FailErr(t, "blob", os.WriteFile(filepath.Join(src, "big.bin"), make([]byte, 5<<20), 0o600))

	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)

	dest := filepath.Join(detectionpack.DevicePacksDir(cfg), "allow-pack")
	var copied []string
	err = filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dest, path)
		copied = append(copied, filepath.ToSlash(rel))
		return nil
	})
	testutil.FailErr(t, "walk dest", err)
	if len(copied) == 0 {
		t.Fatal("import copied no allowlisted files")
	}
	for _, got := range copied {
		ok := got == "pack.yaml" || got == "fixtures.yaml" ||
			(strings.HasPrefix(got, "rules/") && strings.HasSuffix(got, ".yml") && !strings.Contains(got[len("rules/"):], "/"))
		if !ok {
			t.Fatalf("non-allowlisted file copied: %s (all=%v)", got, copied)
		}
	}
	for _, forbidden := range []string{"evil.sh", ".git", "node_modules", "big.bin"} {
		for _, got := range copied {
			if strings.Contains(got, forbidden) {
				t.Fatalf("copied forbidden %s via %s", forbidden, got)
			}
		}
	}
}

// TestDetectionPackImportAtomicOnFailure verifies the public failure path leaves
// no committed or staged pack behind.
func TestDetectionPackImportAtomicOnFailure(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "bad-pack", "not: valid: [[[")
	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	if err == nil {
		t.Fatal("expected invalid pack error")
	}
	deviceRoot := detectionpack.DevicePacksDir(cfg)
	if ents, err := os.ReadDir(deviceRoot); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".import-") || e.Name() == "bad-pack" {
				t.Fatalf("leftover after failed import: %s", e.Name())
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		testutil.FailErr(t, "ReadDir device packs", err)
	}
}

// TestDetectionPackImportedStillAskOnly: imported critical match-all pack cannot allow/deny/suppress.
func TestDetectionPackImportedStillAskOnly(t *testing.T) {
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "user-critical", contractCriticalMatchAllRule())
	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)

	cat, err := detectionpack.LoadCatalog(detectionInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	srcGate := detectionpack.NewGateSource(detectionpack.NewMatcher(cat))
	approvalGate := gateWithDetection(t, gate.PostureBalanced, srcGate)

	res, err := approvalGate.Evaluate(context.Background(), containedCommand("echo hi"))
	testutil.FailErr(t, "Evaluate", err)
	if res.Denied {
		t.Fatalf("imported pack must not deny: %+v", res)
	}
	if res.Required() && res.AutoApproved() {
		t.Fatalf("Required+AutoApproved: %+v", res)
	}

	res, err = approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "s1",
		},
	})
	testutil.FailErr(t, "Evaluate deny", err)
	if !res.Required() || res.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots (not a detection card): %+v", res)
	}
	if res.DetectionCitation != nil {
		t.Fatalf("path escape must not cite a detection: %+v", res.DetectionCitation)
	}
}
