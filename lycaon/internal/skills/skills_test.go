package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidName(t *testing.T) {
	valid := []string{"pdf-processing", "data-analysis", "a", "a1-b2", strings.Repeat("a", 64)}
	for _, s := range valid {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"PDF-Processing",  // uppercase — spec example
		"-pdf",            // leading hyphen — spec example
		"pdf-",            // trailing
		"pdf--processing", // consecutive — spec example
		"",
		strings.Repeat("a", 65),
		"pdf_processing",
		"pdf processing",
	}
	for _, s := range invalid {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false", s)
		}
	}
}

func skillMD(name, desc string, extras string, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	if name != "" {
		b.WriteString("name: " + name + "\n")
	}
	if desc != "" {
		b.WriteString("description: " + desc + "\n")
	}
	b.WriteString(extras)
	b.WriteString("---\n")
	b.WriteString(body)
	return []byte(b.String())
}

func TestParseDescriptionBounds(t *testing.T) {
	ok := skillMD("foo", strings.Repeat("a", 1024), "", "")
	res := Parse("foo", ok)
	if res.Skill.Name == "" || hasNote(res, NoteFieldInvalid) {
		t.Fatalf("1024-char description must load: notes=%v", res.Notes)
	}
	bad := skillMD("foo", strings.Repeat("a", 1025), "", "")
	res = Parse("foo", bad)
	if res.Skill.Name != "" || !hasNote(res, NoteFieldInvalid) {
		t.Fatalf("1025-char description must be fatal: skill=%q notes=%v", res.Skill.Name, res.Notes)
	}
}

func TestParseDescriptionNotTruncated(t *testing.T) {
	desc := strings.Repeat("x", 900)
	res := Parse("foo", skillMD("foo", desc, "", ""))
	if res.Skill.Description != desc {
		t.Fatalf("description mutated: len=%d", len(res.Skill.Description))
	}
}

func TestParseOptionalFields(t *testing.T) {
	content := []byte(`---
name: foo
description: Does a thing when asked.
license: Apache-2.0
compatibility: Needs git
metadata:
  author: example-org
  version: "1.0"
  count: 3
allowed-tools: Bash(rm:*) Read
x-custom: ignored
triggers:
  - a
nested:
  k: v
---
Body line.
`)
	res := Parse("foo", content)
	if res.Skill.Name == "" {
		t.Fatalf("expected load, notes=%v", res.Notes)
	}
	if res.Skill.License != "Apache-2.0" || res.Skill.Compatibility != "Needs git" {
		t.Fatalf("license/compat = %q / %q", res.Skill.License, res.Skill.Compatibility)
	}
	if res.Skill.Metadata["author"] != "example-org" || res.Skill.Metadata["version"] != "1.0" || res.Skill.Metadata["count"] != "3" {
		t.Fatalf("metadata = %#v", res.Skill.Metadata)
	}
	if res.Skill.AllowedTools != "Bash(rm:*) Read" {
		t.Fatalf("allowed-tools = %q", res.Skill.AllowedTools)
	}
	if len(res.Notes) != 0 {
		t.Fatalf("unknown keys must be ignored with zero notes, got %v", res.Notes)
	}
	if res.Skill.Body != "Body line.\n" && res.Skill.Body != "Body line." {
		if !strings.HasPrefix(res.Skill.Body, "Body line.") {
			t.Fatalf("body = %q", res.Skill.Body)
		}
	}
}

func TestParseSpecExamples(t *testing.T) {
	minimal := []byte(`---
name: skill-name
description: A description of what this skill does and when to use it.
---
`)
	res := Parse("skill-name", minimal)
	if res.Skill.Name == "" || res.Skill.Body != "" || len(res.Notes) != 0 {
		t.Fatalf("minimal: skill=%q body=%q notes=%v", res.Skill.Name, res.Skill.Body, res.Notes)
	}

	full := []byte(`---
name: pdf-processing
description: Extract PDF text, fill forms, merge files. Use when handling PDFs.
license: Apache-2.0
metadata:
  author: example-org
  version: "1.0"
---
`)
	res = Parse("pdf-processing", full)
	if res.Skill.Name == "" || len(res.Notes) != 0 {
		t.Fatalf("full example: notes=%v", res.Notes)
	}
	if res.Skill.License != "Apache-2.0" || res.Skill.Metadata["author"] != "example-org" {
		t.Fatalf("full fields = %#v", res.Skill)
	}
}

func TestParseRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"bar", "", strings.Repeat("n", 100)} {
		res := Parse("foo", skillMD(name, "desc", "", ""))
		if res.Skill.Name != "" || !hasNote(res, NoteNameMismatch) {
			t.Fatalf("name %q: skill=%q notes=%v", name, res.Skill.Name, res.Notes)
		}
	}
	res := Parse("My Skill", skillMD("My Skill", "desc", "", ""))
	if res.Skill.Name != "" || !hasNote(res, NoteNameInvalid) {
		t.Fatalf("invalid directory: %#v", res)
	}
}

func TestParseMissingDescription(t *testing.T) {
	res := Parse("foo", []byte("---\nname: foo\n---\n"))
	if res.Skill.Name != "" || !hasNote(res, NoteFieldMissing) {
		t.Fatalf("notes=%v skill=%q", res.Notes, res.Skill.Name)
	}
}

func TestParseRejectsInvalidFrontmatter(t *testing.T) {
	content := []byte(`---
name: foo
description: Use this skill when: the user asks about PDFs
---
`)
	res := Parse("foo", content)
	if res.Skill.Name != "" || !hasNote(res, NoteFrontmatterInvalid) {
		t.Fatalf("invalid frontmatter: %#v", res)
	}
}

func TestParseAcceptsQuotedColon(t *testing.T) {
	content := []byte(`---
name: foo
description: "Use this skill when: the user asks about PDFs"
---
`)
	res := Parse("foo", content)
	if len(res.Notes) != 0 || res.Skill.Name == "" {
		t.Fatalf("quoted value should parse without repair: notes=%v", res.Notes)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	content := []byte("---\nname: foo\ndescription: [unterminated\n---\n")
	res := Parse("foo", content)
	if res.Skill.Name != "" || !hasNote(res, NoteFrontmatterInvalid) {
		t.Fatalf("want frontmatter_invalid, got %#v", res)
	}
}

func TestParseNoFence(t *testing.T) {
	res := Parse("foo", []byte("no fence\n"))
	if res.Skill.Name != "" || !hasNote(res, NoteFrontmatterInvalid) {
		t.Fatalf("%#v", res)
	}
	res = Parse("foo", []byte("---\nname: foo\ndescription: x\n"))
	if res.Skill.Name != "" || !hasNote(res, NoteFrontmatterInvalid) {
		t.Fatalf("no closing fence: %#v", res)
	}
}

func TestParseBOMAndCRLF(t *testing.T) {
	content := []byte("\xEF\xBB\xBF---\r\nname: foo\r\ndescription: desc\r\n---\r\nBody\r\n")
	res := Parse("foo", content)
	if res.Skill.Name == "" || !strings.HasPrefix(res.Skill.Body, "Body") {
		t.Fatalf("%#v", res)
	}
}

func TestParseEmptyBody(t *testing.T) {
	res := Parse("foo", skillMD("foo", "desc", "", ""))
	if res.Skill.Name == "" || res.Skill.Body != "" || len(res.Notes) != 0 {
		t.Fatalf("%#v", res)
	}
}

func TestParseOversizeBody(t *testing.T) {
	content := append([]byte("---\nname: foo\ndescription: d\n---\n"), bytesOf(BodyMax+1)...)
	res := Parse("foo", content)
	if res.Skill.Name != "" || !hasNote(res, NoteTooLarge) {
		t.Fatalf("%#v", res)
	}
}

func TestParseCompatibilityBound(t *testing.T) {
	res := Parse("foo", skillMD("foo", "d", "compatibility: "+strings.Repeat("x", 500)+"\n", ""))
	if res.Skill.Name == "" || len(res.Notes) != 0 {
		t.Fatalf("500 must load clean: %#v", res)
	}
	res = Parse("foo", skillMD("foo", "d", "compatibility: "+strings.Repeat("x", 501)+"\n", ""))
	if res.Skill.Name != "" || !hasNote(res, NoteCompatibilityInvalid) {
		t.Fatalf("501 must be rejected: %#v", res)
	}
}

// stockSkill defines required bundled skill content.
type stockSkill struct {
	packRoot string
	name     string
	ref      string
	desc     string
}

var stockSkills = []stockSkill{
	{packRoot: "platform", name: "apply-a-structural-codemod", ref: "references/structural-rewrite.md"},
	{packRoot: "platform", name: "investigate-code-history", ref: "references/history-routing.md"},
	{packRoot: "platform", name: "verify-a-change", ref: "references/failure-triage.md"},
	{packRoot: "platform", name: "write-a-skill", ref: "references/FORMAT.md"},
	{
		packRoot: "platform",
		name:     "evolve-a-system",
		desc:     "Review failure, retry, and idempotency policies; design state machines and concurrency; evolve APIs or fix systemic defects.",
	},
	{
		packRoot: "platform",
		name:     "trace-a-system-invariant",
		desc:     "Trace a correctness invariant across writers, readers, storage, caches, and state transitions.",
	},
	{
		packRoot: "platform",
		name:     "diagnose-performance",
		desc:     "Investigate slow queries, performance regressions, memory leaks, and disk, memory, or descriptor exhaustion.",
	},
	{
		packRoot: "platform",
		name:     "design-and-debug-tests",
		desc:     "Minimize reproductions, diagnose flaky tests, and design property-based or invariant regression tests.",
	},
	{
		packRoot: "platform",
		name:     "write-extension-pack",
		ref:      "references/pack-layout.md",
		desc:     "Create and validate extension packs containing supported catalog policy, guidance, workflows, agents, or skills.",
	},
	{
		packRoot: "platform",
		name:     "write-workflow",
		ref:      "references/workflow-contract.md",
		desc:     "Author and validate workflow phases, gates, human decisions, and durable orchestration.",
	},
	{
		packRoot: "platform",
		name:     "reach-a-network-service",
		desc:     "Before network access, local service setup, or bind/listen/connect/DNS/proxy recovery, select the confined route.",
	},
	{
		packRoot: "platform",
		name:     "verify-terminal-change",
		ref:      "references/terminal-evidence.md",
		desc:     "Verify changed CLI/TUI output, including non-interactive reports, TTY behavior, and requested terminal images.",
	},
	{
		packRoot: "platform",
		name:     "verify-a-backup-and-restore",
		desc:     "Prove a backup restores required data into an isolated target within the recovery-time objective.",
	},
	{
		packRoot: "platform",
		name:     "prepare-a-release",
		desc:     "Assemble and verify release versions, notes, artifacts, provenance, and clean-install evidence without publishing.",
	},
	{
		packRoot: "platform",
		name:     "write-policy-rule",
		ref:      "references/rule-contract.md",
		desc:     "Author and test Open Agent Rules over supported machine-state anchors and facts without new host behavior.",
	},
	{
		packRoot: "platform",
		name:     "write-prompt-override",
		ref:      "references/prompt-layering.md",
		desc:     "Create and render-test a project or extension override for an existing prompt template or persona.",
	},
	{
		packRoot: "platform",
		name:     "inspect-a-database",
		ref:      "references/inspect_a_redis_instance.md",
	},
	{
		packRoot: "hitl",
		name:     "ask-for-a-decision",
		ref:      "references/decision-cards.md",
		desc:     "Use ask_user for a blocking product choice, required human action, or review of one to four visuals.",
	},
	{
		packRoot: "web-research",
		name:     "research-current-information",
		ref:      "references/research-routing.md",
		desc:     "Research changing versions, APIs, advisories, schedules, prices, laws, or other current external facts.",
	},
	{
		packRoot: "browser",
		name:     "verify-visual-change",
		ref:      "references/page-evidence.md",
		desc:     "Inspect the running UI and capture grounded artifacts for visible changes, requested receipts, or proposals.",
	},
	{
		packRoot: "browser",
		name:     "review-accessibility",
		ref:      "references/review-boundary.md",
		desc:     "Review keyboard, focus, motion, contrast, responsive layout, and structural accessibility on a bounded route.",
	},
	{
		packRoot: "scan-guidance",
		name:     "triage-security-findings",
		ref:      "references/scan-triage-routing.md",
	},
	{
		packRoot: "security",
		name:     "write-detection-pack",
		ref:      "references/sigma-subset.md",
		desc:     "Create and test additive Sigma detections that raise approval asks or egress holds on tool and network events.",
	},
}

var bundledDescriptionSentenceEnd = regexp.MustCompile(`[.!?](?:\s|$)`)

func TestBundledSkillDescriptionsAreOneSentence(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "packs", "painted-wolf", "*", "skills", "*", "SKILL.md"))
	testutil.FailErr(t, "glob bundled skills", err)
	if len(paths) == 0 {
		t.Fatal("no bundled skills found")
	}

	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		testutil.FailErr(t, "read "+path, readErr)
		name := filepath.Base(filepath.Dir(path))
		res := Parse(name, data)
		if res.Skill.Name == "" || len(res.Notes) != 0 {
			t.Fatalf("%s: %#v", path, res)
		}
		if got := len(bundledDescriptionSentenceEnd.FindAllString(res.Skill.Description, -1)); got != 1 {
			t.Errorf("%s description has %d sentence endings, want 1: %q", name, got, res.Skill.Description)
		}
	}
}

func TestStockSkillsOnDisk(t *testing.T) {
	for _, tc := range stockSkills {
		root := filepath.Join("..", "..", "config", "packs", "painted-wolf", tc.packRoot, "skills")
		path := filepath.Join(root, tc.name, "SKILL.md")
		data, err := os.ReadFile(path)
		testutil.FailErr(t, "read "+path, err)
		res := Parse(tc.name, data)
		if res.Skill.Name == "" || len(res.Notes) != 0 {
			t.Fatalf("%s: %#v", tc.name, res)
		}
		if tc.desc != "" && res.Skill.Description != tc.desc {
			t.Fatalf("%s description = %q want %q", tc.name, res.Skill.Description, tc.desc)
		}
		if strings.Contains(string(data), "allowed-tools") {
			t.Fatalf("%s must omit allowed-tools", tc.name)
		}
		if tc.ref == "" {
			continue
		}
		ref := filepath.Join(root, tc.name, filepath.FromSlash(tc.ref))
		if _, err := os.Stat(ref); err != nil {
			testutil.FailErr(t, "stat "+tc.ref, err)
		}
	}
}

func TestAccessibilitySkillClaimsNoCompliance(t *testing.T) {
	a11y := filepath.Join("..", "..", "config", "packs", "painted-wolf", "browser", "skills", "review-accessibility", "SKILL.md")
	body, err := os.ReadFile(a11y)
	testutil.FailErr(t, "read review-accessibility", err)
	lower := strings.ToLower(string(body))
	for _, banned := range []string{"certified", "certification", "wcag compliant"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("review-accessibility must not claim %q", banned)
		}
	}
}

func TestNoExecImport(t *testing.T) {
	data, err := os.ReadFile("skills.go")
	testutil.FailErr(t, "read skills.go", err)
	if strings.Contains(string(data), "os/exec") || strings.Contains(string(data), "exec.") {
		t.Fatal("skills package must not execute")
	}
}

func hasNote(res ParseResult, note string) bool {
	for _, n := range res.Notes {
		if n == note {
			return true
		}
	}
	return false
}

func bytesOf(n int) []byte {
	return []byte(strings.Repeat("x", n))
}
