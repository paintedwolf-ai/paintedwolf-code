//go:build integration

package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpenGrepEmbeddedSourceRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("embedded scanner integration skipped in -short")
	}
	confine.TestingSetAutoConfine(t)
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "load scanner manifest", err)
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "locate scanner home", err)
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}
	scanner := bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
		ID: "embedded-recovery", HomeDir: home, Manifest: manifest, Jobs: 1,
	})
	project := t.TempDir()
	sources := map[string]string{
		"svg.html":     "<svg><script>foreign()</script></svg>\n<script>const code = location.hash; eval(code);</script>",
		"math.html":    "<script>const code = location.hash;</script>\n<math><script>foreign()</script></math>\n<script>eval(code);</script>",
		"recovery.vue": `<template><div>{{ '</div>' }}</div></template><script>const code = location.hash; eval(code);</script>`,
		"before.vue":   `<script>const code = location.hash; eval(code);</script><template><div>{{ '</div>' }}</div></template>`,
		"setup.vue":    `<template><div>{{ '</div>' }}</div></template><script setup lang="ts">const code = location.hash; eval(code);</script>`,
		"syntax.html":  "<div>狼</div>\r\n<script>const code = location.hash; eval(code);\r\nif (\r\n</script>",
		"quiet.html":   "<svg><script>eval(location.hash)</script></svg><script>eval('fixed');</script>",
		"quiet.vue":    `<template><div>{{ '</div>' }}<script>eval(location.hash)</script></div></template><script>eval('fixed');</script>`,
	}
	for name, source := range sources {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(project, name), []byte(source), 0o600))
	}
	result, err := scanner.Run(t.Context(), scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
	testutil.FailErr(t, "scan independent embedded scripts", err)
	findings := make(map[string]int)
	for _, finding := range result.Findings {
		if len(finding.Locations) != 1 {
			t.Fatalf("primary locations=%+v", finding.Locations)
		}
		findings[filepath.Base(finding.Locations[0].URI)]++
		scanfindings.VisitFindingLocations(&finding, func(location *api.SecurityFindingLocation) {
			original, exists := sources[filepath.Base(location.URI)]
			if !exists || strings.Contains(location.URI, ".script-") {
				t.Fatalf("unmapped evidence=%+v", location)
			}
			text := embeddedEvidenceText(t, original, *location)
			if text != "code" && text != "location.hash" && text != "eval(code)" {
				t.Fatalf("evidence differs from original source: %q %+v", text, location)
			}
		})
	}
	for name := range sources {
		want := 1
		if name == "quiet.html" || name == "quiet.vue" {
			want = 0
		}
		if findings[name] != want {
			t.Errorf("%s findings=%d want=%d", name, findings[name], want)
		}
	}
	warnings := make(map[string]int)
	for _, warning := range result.Warnings {
		name := filepath.Base(warning.File)
		warnings[name]++
		if _, exists := sources[name]; !exists || strings.Contains(warning.Message, "paintedwolf-opengrep-") || strings.Contains(warning.Message, ".script-") {
			t.Errorf("unmapped warning=%+v", warning)
		}
		if name == "syntax.html" && (warning.StartLine != 3 || warning.StartColumn != 1) {
			t.Errorf("syntax position=%+v", warning)
		}
	}
	for name := range sources {
		if warnings[name] != 1 {
			t.Errorf("%s warnings=%d want=1", name, warnings[name])
		}
	}
}

func embeddedEvidenceText(t *testing.T, source string, location api.SecurityFindingLocation) string {
	t.Helper()
	lines := bytes.Split([]byte(source), []byte{'\n'})
	offset := func(line, column int) int {
		if line < 1 || line > len(lines) || column < 1 || column > len(lines[line-1])+1 {
			t.Fatalf("evidence outside original bytes: %+v", location)
		}
		result := column - 1
		for _, value := range lines[:line-1] {
			result += len(value) + 1
		}
		return result
	}
	start, end := offset(location.StartLine, location.StartColumn), offset(location.EndLine, location.EndColumn)
	if start > end {
		t.Fatalf("reversed evidence: %+v", location)
	}
	return source[start:end]
}
