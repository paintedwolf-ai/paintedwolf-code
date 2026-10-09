package skills_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillsReadHappyPath(t *testing.T) {
	body := "# Guide\n\nDo the thing.\n"
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{
				Name: "verify-a-change",
				Body: body,
				Dir:  "/abs/skills/verify-a-change",
				Resources: []string{
					"references/FORMAT.md",
					"scripts/check.sh",
				},
			}}
		},
	}
	got, err := tool.Run(context.Background(), map[string]any{"need": "verify-a-change"}, tools.ToolContext{})
	testutil.FailErr(t, "tool.Run failed", err)
	want := "Skill: verify-a-change\n\n" + body + "\nSkill directory: /abs/skills/verify-a-change\nFiles: references/FORMAT.md, scripts/check.sh\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSkillsReadRequiresNeed(t *testing.T) {
	tool := &skilltools.SkillsReadTool{Skills: func(context.Context, tools.ToolContext) []skills.Skill {
		return []skills.Skill{{Name: "verify-a-change"}}
	}}
	if _, err := tool.Run(context.Background(), map[string]any{}, tools.ToolContext{}); err == nil {
		t.Fatal("missing need must reject")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"resource": "references/x.md"}, tools.ToolContext{}); err == nil {
		t.Fatal("resource without need must reject")
	}
}

func TestSkillsReadResourceEnvelope(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{
				{Name: "empty", Body: "body\n", Dir: "/d/empty"},
				{Name: "overflow", Body: "body\n", Dir: "/d/overflow",
					Resources: []string{"a.md", "b.md"}, ResourcesOmitted: 3},
			}
		},
	}
	empty, err := tool.Run(context.Background(), map[string]any{"need": "empty"}, tools.ToolContext{})
	testutil.FailErr(t, "tool.Run failed", err)
	if strings.Contains(empty, "Files:") {
		t.Fatalf("empty resources must omit Files line: %q", empty)
	}
	overflow, err := tool.Run(context.Background(), map[string]any{"need": "overflow"}, tools.ToolContext{})
	testutil.FailErr(t, "tool.Run failed", err)
	if !strings.Contains(overflow, "Files: a.md, b.md (+3 more)\n") {
		t.Fatalf("overflow envelope = %q", overflow)
	}
}

func TestSkillsReadResourceTemplateBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project bool
		marked  bool
		want    string
	}{
		{name: "pack opt in", marked: true, want: "budget 7, mode bounded"},
		{name: "literal pack reference", want: "budget {{ test_budget }}, mode {{ configuration.mode }}"},
		{name: "literal project reference", project: true, marked: true, want: "budget {{ test_budget }}, mode {{ configuration.mode }}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const resource = "references/guide.md"
			resourceFS := fstest.MapFS{resource: {Data: []byte("budget {{ test_budget }}, mode {{ configuration.mode }}")}}
			sk := skills.Skill{Name: "example", PackID: "example/pack", Project: tc.project}
			if tc.marked {
				sk.Metadata = map[string]string{skills.TemplateResourcesMetadata: resource}
			}
			sk.BindResources(resourceFS, ".", skills.DiscoverBundledResources(resourceFS, "."), "")
			tool := &skilltools.SkillsReadTool{
				Skills:       func(context.Context, tools.ToolContext) []skills.Skill { return []skills.Skill{sk} },
				TemplateVars: func(context.Context, tools.ToolContext) map[string]any { return map[string]any{"test_budget": 7} },
				PackConfiguration: func(context.Context, tools.ToolContext, string) map[string]any {
					return map[string]any{"mode": "bounded"}
				},
			}
			out := &tools.ToolInvocationOut{}
			got, err := tool.Run(t.Context(), map[string]any{"need": "example", "resource": resource}, tools.ToolContext{Out: out})
			testutil.FailErr(t, "read reference", err)
			if got != "Skill resource: example/"+resource+"\n\n"+tc.want+"\n" || out.Skill == nil || out.Skill.Instructions != tc.want {
				t.Fatalf("resource output = %q; projection = %#v", got, out.Skill)
			}
		})
	}
}

func TestSkillsReadReturnsListedResource(t *testing.T) {
	resourceFS := fstest.MapFS{
		"references/guide.md": {Data: []byte("# Guide\n\nFollow this.\n")},
	}
	sk := skills.Skill{Name: "example", Body: "instructions\n"}
	sk.BindResources(resourceFS, ".", skills.DiscoverBundledResources(resourceFS, "."), "")
	tool := &skilltools.SkillsReadTool{Skills: func(context.Context, tools.ToolContext) []skills.Skill {
		return []skills.Skill{sk}
	}}
	got, err := tool.Run(context.Background(), map[string]any{
		"need": "example", "resource": "references/guide.md",
	}, tools.ToolContext{})
	testutil.FailErr(t, "resource read", err)
	if got != "Skill resource: example/references/guide.md\n\n# Guide\n\nFollow this.\n" {
		t.Fatalf("resource envelope = %q", got)
	}
	_, err = tool.Run(context.Background(), map[string]any{
		"need": "example", "resource": "../outside.md",
	}, tools.ToolContext{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SKILL_RESOURCE_UNKNOWN" {
		t.Fatalf("invalid resource err = %v", err)
	}
}

// Skill card fields come from the catalog stamp, not from re-parsing the envelope.
func TestSkillsReadStampsSkillProjection(t *testing.T) {
	sk := skills.Skill{
		Name:             "verify-a-change",
		Description:      "Run the scoped checks before handoff.",
		Body:             "# Verify\n\nRun the scoped checks.\n",
		Dir:              "/abs/skills/verify-a-change",
		Resources:        []string{"references/FORMAT.md"},
		ResourcesOmitted: 2,
		Project:          true,
		PackID:           "painted-wolf/platform",
		License:          "Apache-2.0",
		Compatibility:    "any",
		AllowedTools:     "read, grep",
	}
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{sk}
		},
	}
	out := &tools.ToolInvocationOut{}
	_, err := tool.Run(
		context.Background(),
		map[string]any{"need": "verify-a-change"},
		tools.ToolContext{Out: out},
	)
	testutil.FailErr(t, "tool.Run failed", err)
	got := out.Skill
	if got == nil {
		t.Fatal("skills_read must stamp the skill projection")
	}
	want := api.SkillActivation{
		Name:             sk.Name,
		Description:      sk.Description,
		Instructions:     sk.Body,
		Dir:              sk.Dir,
		Resources:        []string{"references/FORMAT.md"},
		ResourcesOmitted: 2,
		Project:          true,
		PackID:           sk.PackID,
		License:          sk.License,
		Compatibility:    sk.Compatibility,
		AllowedTools:     sk.AllowedTools,
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("projection:\n got %#v\nwant %#v", *got, want)
	}
	// Instructions carry the authored body only. The card composes its own
	// directory and files rows, so a trailer here would render them twice.
	if strings.Contains(got.Instructions, "Skill directory:") {
		t.Fatalf("instructions must exclude the envelope trailer: %q", got.Instructions)
	}
}

// The projection has an independent slice, so a later catalog mutation leaves what a
// settled transcript row says the activation listed.
func TestSkillsReadProjectionCopiesResources(t *testing.T) {
	resources := []string{"references/FORMAT.md"}
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{Name: "s", Body: "b", Dir: "/d", Resources: resources}}
		},
	}
	out := &tools.ToolInvocationOut{}
	_, err := tool.Run(context.Background(), map[string]any{"need": "s"}, tools.ToolContext{Out: out})
	testutil.FailErr(t, "tool.Run failed", err)
	resources[0] = "mutated.md"
	if out.Skill.Resources[0] != "references/FORMAT.md" {
		t.Fatalf("projection aliases the catalog slice: %v", out.Skill.Resources)
	}
}

func TestSkillsReadUnknownLeavesProjectionUnstamped(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{Name: "foo", Body: "x", Dir: "/d/foo"}}
		},
	}
	out := &tools.ToolInvocationOut{}
	_, err := tool.Run(context.Background(), map[string]any{"need": "missing"}, tools.ToolContext{Out: out})
	testutil.FailErr(t, "discover missing skill", err)
	if out.Skill != nil {
		t.Fatalf("rejected activation must resolve no skill: %#v", out.Skill)
	}
}

// Callers that collect no side outputs (worker paths, direct invocation) pass a
// nil Out; stamping is optional and does not panic the tool.
func TestSkillsReadWithoutCaptureChannel(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{Name: "s", Body: "b\n", Dir: "/d"}}
		},
	}
	got, err := tool.Run(context.Background(), map[string]any{"need": "s"}, tools.ToolContext{})
	testutil.FailErr(t, "tool.Run failed", err)
	if !strings.Contains(got, "Skill directory: /d") {
		t.Fatalf("envelope = %q", got)
	}
}

func TestSkillsReadUnknownAndCase(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{Name: "foo", Body: "x", Dir: "/d/foo"}}
		},
	}
	output, err := tool.Run(context.Background(), map[string]any{"need": "missing"}, tools.ToolContext{})
	testutil.FailErr(t, "discover skill", err)
	var result struct {
		Discovery *turnload.Discovery `json:"discovery"`
	}
	testutil.FailErr(t, "decode discovery", json.Unmarshal([]byte(output), &result))
	if result.Discovery == nil || result.Discovery.Status != "ranking_unavailable" {
		t.Fatalf("discovery = %s", output)
	}
	got, err := tool.Run(context.Background(), map[string]any{"need": "Foo"}, tools.ToolContext{})
	if err != nil || !strings.HasPrefix(got, "Skill: foo\n\n") {
		t.Fatalf("case-insensitive identifier: output=%q err=%v", got, err)
	}
}

func TestSkillsReadNeedOpensOneSkill(t *testing.T) {
	catalog := []skills.Skill{
		{Name: "publish-a-hugo-site", Description: "Build and launch a Hugo site locally.", Body: "Publish safely.\n"},
		{Name: "commit-in-logical-groups", Description: "Stage and commit related changes together.", Body: "Commit carefully.\n"},
	}
	tool := &skilltools.SkillsReadTool{Skills: func(context.Context, tools.ToolContext) []skills.Skill { return catalog }}
	got, err := tool.Run(context.Background(), map[string]any{"need": "commit-in-logical-groups"}, tools.ToolContext{})
	testutil.FailErr(t, "exact read", err)
	if !strings.HasPrefix(got, "Skill: commit-in-logical-groups\n\nCommit carefully.") {
		t.Fatalf("exact read = %q", got)
	}
	for _, need := range []string{"commit the changes", "frobnicate"} {
		output, err := tool.Run(context.Background(), map[string]any{"need": need}, tools.ToolContext{})
		testutil.FailErr(t, "discover without ranking", err)
		if !strings.Contains(output, `"discovery"`) || strings.Contains(output, "Commit carefully") {
			t.Fatalf("discovery read a body: %s", output)
		}
	}
	tool.Lookup = func(_ context.Context, _ tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
		return turnload.LookupOutcome{Need: need, Ranked: &turnload.SkillRank{Name: "publish-a-hugo-site", Score: 3.5}}
	}
	got, err = tool.Run(context.Background(), map[string]any{"need": "launch the docs site"}, tools.ToolContext{})
	testutil.FailErr(t, "ranked read", err)
	if !strings.HasPrefix(got, "Skill: publish-a-hugo-site\n\nPublish safely.") {
		t.Fatalf("ranked read = %q", got)
	}
}

func TestSkillsReadSourceTouchesNoFilesystem(t *testing.T) {
	data, err := os.ReadFile("skills_read.go")
	testutil.FailErr(t, "read file", err)
	src := string(data)
	for _, banned := range []string{"os.", "projectpaths", "filepath.Join", "os/exec", "exec."} {
		if strings.Contains(src, banned) {
			t.Fatalf("skills_read.go must not contain %q", banned)
		}
	}
}

func TestSkillsReadRendersPackBody(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{
				Name:   "orchestrate-a-large-task",
				Body:   "Worker loops: {{ worker_tool_budget_default }}. Hunt: {{ hunt_wave_workers }}.\n",
				Dir:    "/pack/skills/orchestrate-a-large-task",
				PackID: "painted-wolf/platform",
			}}
		},
	}
	out := &tools.ToolInvocationOut{}
	got, err := tool.Run(
		context.Background(),
		map[string]any{"need": "orchestrate-a-large-task"},
		tools.ToolContext{Out: out},
	)
	testutil.FailErr(t, "tool.Run failed", err)
	wantBody := fmt.Sprintf(
		"Worker loops: %d. Hunt: %d.\n",
		spawn.DefaultWorkerMaxToolLoops,
		spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())["hunt_wave_workers"],
	)
	if !strings.Contains(got, wantBody) {
		t.Fatalf("envelope missing rendered body %q:\n%s", wantBody, got)
	}
	if out.Skill == nil || out.Skill.Instructions != wantBody {
		t.Fatalf("stamp Instructions = %q want %q", out.Skill.Instructions, wantBody)
	}
}

func TestSkillsReadProjectBodyStaysVerbatim(t *testing.T) {
	body := "Literal {{ max_tool_loops }} and {% if true %}x{% endif %}.\n"
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{
				Name:    "project-skill",
				Body:    body,
				Dir:     "/proj/.paintedwolf/skills/project-skill",
				Project: true,
			}}
		},
	}
	out := &tools.ToolInvocationOut{}
	got, err := tool.Run(
		context.Background(),
		map[string]any{"need": "project-skill"},
		tools.ToolContext{Out: out},
	)
	testutil.FailErr(t, "tool.Run failed", err)
	if !strings.Contains(got, body) {
		t.Fatalf("project skill must stay verbatim:\n%s", got)
	}
	if out.Skill.Instructions != body {
		t.Fatalf("stamp = %q want %q", out.Skill.Instructions, body)
	}
}

func TestSkillsReadTemplateInvalid(t *testing.T) {
	tool := &skilltools.SkillsReadTool{
		Skills: func(context.Context, tools.ToolContext) []skills.Skill {
			return []skills.Skill{{
				Name: "broken",
				Body: "{% if true %}\n",
				Dir:  "/pack/skills/broken",
			}}
		},
	}
	_, err := tool.Run(context.Background(), map[string]any{"need": "broken"}, tools.ToolContext{})
	var tr *toolrejection.ToolReject
	if !errors.As(err, &tr) || tr.Code != "SKILL_TEMPLATE_INVALID" {
		t.Fatalf("err=%v want SKILL_TEMPLATE_INVALID", err)
	}
	if name, _ := tr.Data["name"].(string); name != "broken" {
		t.Fatalf("name = %#v", tr.Data["name"])
	}
}
