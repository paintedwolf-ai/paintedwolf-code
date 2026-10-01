package blueprint_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSplitMarkdownFrontmatterMissing(t *testing.T) {
	body := "## Goal\n\nShip it.\n"
	meta, got := blueprintfile.SplitMarkdownFrontmatter(body)
	if meta != nil {
		t.Fatalf("meta = %#v want nil", meta)
	}
	if got != body {
		t.Fatalf("body = %q", got)
	}
	if blueprint.ParseStatusFrontmatter(body) != api.BlueprintStatusDraft {
		t.Fatal("missing frontmatter must default to draft")
	}
}

func TestSplitMarkdownFrontmatterUnclosedHR(t *testing.T) {
	// Leading markdown HR must not swallow the document as "meta".
	content := "---\n\n## Goal\n\nShip it.\n"
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if meta != nil {
		t.Fatalf("meta = %#v want nil for unclosed fence", meta)
	}
	if body != content {
		t.Fatalf("body rewritten: %q", body)
	}
}

func TestSplitMarkdownFrontmatterCorruptYAML(t *testing.T) {
	content := "---\nstatus: [broken\ntitle: x\n---\n## Goal\n\nOk.\n"
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if len(meta) != 0 {
		t.Fatalf("corrupt YAML must yield empty meta, got %#v", meta)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "## Goal") {
		t.Fatalf("body = %q", body)
	}
	if blueprint.ParseStatusFrontmatter(content) != api.BlueprintStatusDraft {
		t.Fatal("corrupt status must default to draft")
	}
}

func TestSetStatusFrontmatterRepairsCorrupt(t *testing.T) {
	content := "---\nstatus: [broken\n---\n## Goal\n\nOk.\n"
	out := blueprint.SetStatusFrontmatter(content, api.BlueprintStatusApproved)
	if blueprint.ParseStatusFrontmatter(out) != api.BlueprintStatusApproved {
		t.Fatalf("status not repaired: %q", out)
	}
	if !strings.Contains(out, "## Goal") {
		t.Fatalf("body lost: %q", out)
	}
}

func TestSetStatusFrontmatterAddsWhenMissing(t *testing.T) {
	content := "## Goal\n\nOk.\n"
	out := blueprint.SetStatusFrontmatter(content, api.BlueprintStatusApproved)
	if blueprint.ParseStatusFrontmatter(out) != api.BlueprintStatusApproved {
		t.Fatalf("status = %q", blueprint.ParseStatusFrontmatter(out))
	}
	if !strings.Contains(out, "## Goal") {
		t.Fatalf("body lost: %q", out)
	}
}

func TestEncodeFrontmatterKeyOrderIsStable(t *testing.T) {
	content := "---\nresearch_depth: none\nstatus: draft\ntitle: Ship it\n---\n## Goal\n"
	var last string
	for i := 0; i < 20; i++ {
		out := blueprint.SetStatusFrontmatter(content, api.BlueprintStatusDraft)
		if last != "" && out != last {
			t.Fatalf("frontmatter order drifted:\n%s\n---\n%s", last, out)
		}
		last = out
		content = out
	}
	if !strings.HasPrefix(last, "---\nstatus: draft\ntitle: Ship it\nresearch_depth: none\n---\n") {
		t.Fatalf("canonical order missing: %q", last)
	}
}

func TestParseTitleIgnoresCorrupt(t *testing.T) {
	content := "---\ntitle: [oops\n---\n## Goal\n"
	if blueprint.ParseTitleFrontmatter(content) != "" {
		t.Fatal("corrupt title must be empty")
	}
}
