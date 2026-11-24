package reach_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/archlint/reach"
	"github.com/lycaon/lycaon/internal/testutil"
)

func emitterSpec() reach.EmitterSpec {
	return reach.EmitterSpec{
		Registry: "registry.json",
		Policies: "packs",
		Data:     []string{"rules"},
		Facts:    "facts.yaml",
		Anchors:  "anchors.yaml",
		Plumbing: []string{"facts/declarations.go"},
		Derived:  []string{"paintedwolf.derived_"},
	}
}

func TestRunReportsWhatTheProductNeverEmits(t *testing.T) {
	root, err := filepath.Abs("testdata/emitters")
	testutil.FailErr(t, "filepath.Abs failed", err)
	emitters, err := reach.LoadEmitters(root, emitterSpec())
	testutil.FailErr(t, "load emitters", err)
	got, err := reach.Run(reach.Config{
		Dir:      filepath.Join(root, "mod"),
		Patterns: []string{"./cmd/..."},
		GOOS:     []string{"linux", "darwin"},
		Report:   regexp.MustCompile(`^example\.com/emitters/`),
		Product:  []string{"./cmd/app"},
		Emitters: emitters,
	})
	testutil.FailErr(t, "reach.Run failed", err)
	var keys []string
	for _, f := range got {
		switch f.Kind {
		case reach.KindUnemittedCode, reach.KindUnproducedFact, reach.KindUnfiredAnchor:
			keys = append(keys, f.Kind+" "+f.Key)
		}
	}
	sort.Strings(keys)
	want := []string{
		"unemitted-code DEAD_CODE",
		"unemitted-code ECHOED",
		"unemitted-code ONLY_COMPARED",
		"unemitted-code TOOL_ONLY_CODE",
		"unemitted-code UNFIRED_POLICY",
		"unfired-anchor anchor.unfired",
		"unproduced-fact paintedwolf.declared_only",
		"unproduced-fact paintedwolf.never_field",
	}
	if len(keys) != len(want) {
		t.Fatalf("findings = %q, want %q", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("findings = %q, want %q", keys, want)
		}
	}
}

func TestRunRefusesEmittersWithoutProduct(t *testing.T) {
	root, err := filepath.Abs("testdata/emitters")
	testutil.FailErr(t, "filepath.Abs failed", err)
	emitters, err := reach.LoadEmitters(root, emitterSpec())
	testutil.FailErr(t, "load emitters", err)
	_, err = reach.Run(reach.Config{Dir: filepath.Join(root, "mod"), Patterns: []string{"./cmd/..."}, GOOS: []string{"linux"}, Emitters: emitters})
	if err == nil {
		t.Fatal("judged emitters without product roots")
	}
}

func TestLoadEmittersRefusesIncompleteDeclarations(t *testing.T) {
	root, err := filepath.Abs("testdata/emitters")
	testutil.FailErr(t, "filepath.Abs failed", err)
	for name, edit := range map[string]func(*reach.EmitterSpec){
		"missing registry": func(s *reach.EmitterSpec) { s.Registry = "absent.json" },
		"no policies":      func(s *reach.EmitterSpec) { s.Policies = "rules" },
		"no facts":         func(s *reach.EmitterSpec) { s.Facts = "anchors.yaml" },
		"no anchors":       func(s *reach.EmitterSpec) { s.Anchors = "facts.yaml" },
		"missing data":     func(s *reach.EmitterSpec) { s.Data = []string{"absent"} },
	} {
		spec := emitterSpec()
		edit(&spec)
		if _, err := reach.LoadEmitters(root, spec); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestLoadEmittersTracksHostBindingHintCodes(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(p), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(p, []byte(body), 0o600))
	}
	write("registry.json", `{"hint_codes":{"FIRED_HINT":{"summary":"ok"},"UNFIRED_HINT":{"summary":"ok"}}}`)
	write("packs/platform/policy/FIRED_HINT.yaml", "spec_version: \"1.0\"\nid: FIRED_HINT\nkind: hint\nmessage: ok\n")
	write("packs/platform/host/bindings/fired.yaml", "'on': anchor.fired\neffect: inform\nrender: coordinator-fired\nhint_code: FIRED_HINT\n")
	write("packs/platform/host/bindings/unfired.yaml", "'on': anchor.unfired\neffect: inform\nrender: coordinator-unfired\nhint_code: UNFIRED_HINT\n")
	write("packs/platform/host/bindings/template.yaml", "'on': anchor.fired\neffect: inform\nrender: coordinator-tpl\n")
	write("facts.yaml", "host_facts:\n  - name: paintedwolf.f\n")
	write("anchors.yaml", "anchors:\n  - id: anchor.fired\n  - id: anchor.unfired\n")

	emitters, err := reach.LoadEmitters(dir, reach.EmitterSpec{
		Registry: "registry.json",
		Policies: "packs",
		Facts:    "facts.yaml",
		Anchors:  "anchors.yaml",
	})
	testutil.FailErr(t, "load emitters with host bindings", err)
	if emitters == nil {
		t.Fatal("expected non-nil emitters")
	}
}
