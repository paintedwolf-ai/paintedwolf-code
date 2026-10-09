package profiles

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSkillHostResourcesGateAndContext(t *testing.T) {
	t.Setenv("LYCAON_CAPABILITY_TEST_PRESENT", "1")
	catalog := []byte(`version: 1
resources:
  - id: present
    family: test
    label: Present
    category: Test
    description: Present host resource.
    surfaces: [process_exec]
    realizations:
      - discover:
          environment:
            names: [LYCAON_CAPABILITY_TEST_PRESENT]
        connections:
          - mode: proxy
  - id: absent
    family: test
    label: Absent
    category: Test
    description: Absent host resource.
    surfaces: [process_exec]
    realizations:
      - discover:
          path:
            kind: file
            paths: ["/definitely/not/present"]
`)
	configtest.Only(t, map[config.Rel]string{config.HostResources: string(catalog)})
	service, err := hostresources.NewService(t.TempDir())
	testutil.FailErr(t, "new host resources service", err)
	snapshot := service.SnapshotFor(context.Background(), false, hostresources.ProjectContext{}, nil)
	surfaces := []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	loaded, diags := applySkillHostResources([]skills.Skill{
		{Name: "plain", Metadata: map[string]string{}, Body: "plain"},
		{Name: "ready", UnitID: "skills/ready", Metadata: map[string]string{"host_resources": "present"}, Body: "ready"},
		{Name: "hidden", UnitID: "skills/hidden", Metadata: map[string]string{"host_resources": "absent"}, Body: "hidden"},
	}, nil, snapshot, surfaces)
	if len(loaded) != 2 || len(diags) != 1 {
		t.Fatalf("loaded=%#v diags=%#v", loaded, diags)
	}
	if loaded[1].Name != "ready" || !strings.Contains(loaded[1].Body, "routes=proxy") {
		t.Fatalf("decorated skill = %#v", loaded[1])
	}
	if diags[0].UnitID != "skills/hidden" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}

	grouped, groupDiags := applySkillHostResources([]skills.Skill{
		{Name: "either", UnitID: "skills/either", Metadata: map[string]string{"host_resources": "present|absent"}, Body: "either"},
		{Name: "neither", UnitID: "skills/neither", Metadata: map[string]string{"host_resources": "absent|missing"}, Body: "neither"},
	}, nil, snapshot, surfaces)
	if len(grouped) != 1 || len(groupDiags) != 1 {
		t.Fatalf("grouped=%#v diags=%#v", grouped, groupDiags)
	}
	if grouped[0].Name != "either" || !strings.Contains(grouped[0].Body, "id=present") {
		t.Fatalf("any-of decorated skill = %#v", grouped[0])
	}
	if strings.Contains(grouped[0].Body, "id=absent") {
		t.Fatalf("unavailable alternative must not decorate: %s", grouped[0].Body)
	}
	if groupDiags[0].UnitID != "skills/neither" || !strings.Contains(groupDiags[0].Message, "absent|missing") {
		t.Fatalf("group diagnostic = %#v", groupDiags[0])
	}
}
