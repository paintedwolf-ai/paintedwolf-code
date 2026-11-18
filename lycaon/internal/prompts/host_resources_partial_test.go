package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostResourcesPartialGroupsPresenceAndCap(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
		HostResources: []prompts.AgentHostResourceView{
			{ID: "aws-cli", Label: "AWS CLI", Category: "Cloud", Status: "available", Access: "allow", Guidance: "avoid"},
			{ID: "docker", Label: "Docker", Category: "Containers", Status: "available", Access: "allow", Guidance: "omit"},
			{ID: "kubectl", Label: "kubectl", Category: "Kubernetes", Status: "available", Access: "ask", Guidance: "omit"},
		},
		HostResourcesOmitted: 2,
	})
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		Visible:   []string{"read", "write", "edit", "command", "skills_read"},
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader", err)
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", reader, vars)
	testutil.FailErr(t, "RenderPersona", err)
	if !strings.Contains(got, "## Host resources") {
		t.Fatal("expected Host resources section")
	}
	cloud := strings.Index(got, "- Cloud:")
	containers := strings.Index(got, "- Containers:")
	if cloud < 0 || containers < 0 || cloud > containers {
		t.Fatalf("categories not grouped: %q", excerpt(got, "Host resources"))
	}
	if !strings.Contains(got, "`docker` allow") {
		t.Fatal("missing docker presence line")
	}
	if !strings.Contains(got, "(ask)") || !strings.Contains(got, "(avoid unless the user opts in)") {
		t.Fatal("missing ask/avoid annotations")
	}
	if !strings.Contains(got, "This list is capped: 2 more observed resources are not shown here.") {
		t.Fatal("omitted host resources must be counted")
	}
}

func TestHostResourcesPartialOmitsEmptyInventory(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		Visible:   []string{"read", "write", "edit", "command"},
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader", err)
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", reader, nil)
	testutil.FailErr(t, "RenderPersona", err)
	if strings.Contains(got, "## Host resources") {
		t.Fatal("empty inventory must omit Host resources")
	}
}

func excerpt(body, needle string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return body[:min(400, len(body))]
	}
	end := min(len(body), idx+400)
	return body[idx:end]
}
