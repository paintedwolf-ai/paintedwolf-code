package filekind_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/logoutline"
)

func TestDetectTier1Extension(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename: "main.go",
		Mode:     filekind.DepthDeep,
	})
	if res.Tier != filekind.TierExtension {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierExtension)
	}
	if res.Grammar == nil || res.Grammar.Name != "go" {
		t.Fatalf("grammar = %+v", res.Grammar)
	}
}

func TestDetectUnknownExtensionFallsThroughToRegexTier(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename: "notes.qqq",
		Mode:     filekind.DepthShallow,
	})
	if res.Tier != filekind.TierRegexDegraded {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierRegexDegraded)
	}
	if res.Grammar != nil {
		t.Fatal("grammar set for unknown extension")
	}
}

func TestDetectGenericTextDoesNotClaimVimHelp(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "notes.txt",
		HeadSample: []byte("ordinary prose\nwithout Vim help markers\n"),
		Mode:       filekind.DepthDeep,
	})
	if res.Tier != filekind.TierRegexDegraded {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierRegexDegraded)
	}
	if res.Grammar != nil {
		t.Fatalf("grammar = %+v want none", res.Grammar)
	}
}

func TestDetectExactTextFilenameKeepsSpecificGrammar(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename: "requirements.txt",
		Mode:     filekind.DepthShallow,
	})
	if res.Tier != filekind.TierExtension {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierExtension)
	}
	if res.Grammar == nil || res.Grammar.Name != "requirements" {
		t.Fatalf("grammar = %+v want requirements", res.Grammar)
	}
}

func TestDetectTier2ShebangWithoutExtension(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "deploy",
		HeadSample: []byte("#!/usr/bin/env python3\nimport os\n"),
		Mode:       filekind.DepthDeep,
	})
	if res.Tier != filekind.TierShebang {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierShebang)
	}
	if res.Grammar == nil || res.Grammar.Name != "python" {
		t.Fatalf("grammar = %+v", res.Grammar)
	}
}

func TestDetectTier3MisnamedGoSource(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "notes.qqq",
		HeadSample: []byte("package main\n\nfunc main() {}\n"),
		Mode:       filekind.DepthDeep,
	})
	if res.Tier != filekind.TierParseConfidence {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierParseConfidence)
	}
	if res.Grammar == nil || res.Grammar.Name != "go" {
		t.Fatalf("grammar = %+v", res.Grammar)
	}
}

func TestDetectDepthShallowSkipsParseConfidence(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "notes.qqq",
		HeadSample: []byte("package main\n\nfunc main() {}\n"),
		Mode:       filekind.DepthShallow,
	})
	if res.Tier != filekind.TierRegexDegraded {
		t.Fatalf("tier = %d want %d (shallow must not run tier 3)", res.Tier, filekind.TierRegexDegraded)
	}
	if res.Grammar != nil {
		t.Fatal("grammar set under DepthShallow for misnamed go")
	}
}

func TestDetectAmbiguousProseDeclinesToRegexTier(t *testing.T) {
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "memo.qqq",
		HeadSample: []byte("This is plain prose.\nNo code here.\n"),
		Mode:       filekind.DepthDeep,
	})
	if res.Tier != filekind.TierRegexDegraded {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierRegexDegraded)
	}
}

func TestDetectTier1ParityWithExtensionDetectLanguage(t *testing.T) {
	for _, name := range []string{"main.go", "app.py", "index.ts", "lib.rs", "readme.md"} {
		res := filekind.Detect(t.Context(), filekind.DetectReq{Filename: name, Mode: filekind.DepthShallow})
		if res.Tier != filekind.TierExtension {
			t.Fatalf("%s tier = %d want extension", name, res.Tier)
		}
		if res.Grammar == nil {
			t.Fatalf("%s grammar nil", name)
		}
	}
}

func TestGenericRuleLanguagesAreSupportedByPath(t *testing.T) {
	tests := map[string]string{
		"lib/App.pm":      "perl",
		"app.psgi":        "perl",
		"t/service.t":     "perl",
		"script.plx":      "perl",
		"script.perl":     "perl",
		"lib/auto/Foo.al": "perl",
		"include/foo.ph":  "perl",
		"cpanfile":        "perl",
		"Rexfile":         "perl",
		"deploy.ps1":      "powershell",
		"build.gradle":    "groovy",
	}
	for path, want := range tests {
		if got := filekind.LanguageForPath(path); got != want {
			t.Errorf("LanguageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDetectTier4RealJSONLog(t *testing.T) {
	sample := []byte(`{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"started"}` + "\n")
	sample = append(sample, []byte(`{"level":"error","ts":"2024-01-15T10:01:00Z","msg":"failed"}`+"\n")...)
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "app.log",
		HeadSample: sample,
		Mode:       filekind.DepthDeep,
	})
	if res.Tier != filekind.TierLogClassifier {
		t.Fatalf("tier = %d want %d", res.Tier, filekind.TierLogClassifier)
	}
	if res.Log != logoutline.FormatJSONLines {
		t.Fatalf("log = %q want json_lines", res.Log)
	}
}

func TestDetectDepthShallowSkipsRealJSONLog(t *testing.T) {
	sample := []byte(`{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"started"}` + "\n")
	res := filekind.Detect(t.Context(), filekind.DetectReq{
		Filename:   "app.log",
		HeadSample: sample,
		Mode:       filekind.DepthShallow,
	})
	if res.Tier == filekind.TierLogClassifier {
		t.Fatalf("tier = %d want not log classifier under shallow", res.Tier)
	}
}
