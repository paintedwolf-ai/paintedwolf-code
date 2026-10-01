import { stubClient } from "../../test/client-fixture.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { registerProjectsStore } from "../../platform/connection/app-connection.ts";
import { refreshWorkflowState } from "./workflow-actions.ts";

describe("workflow integration", () => {
  it("refreshWorkflowState updates active run and catalog", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const projects = createProjectsStore(() => null, {
      onProjectEvent: () => () => {},
    });
    projects.load([wireProject("/tmp/p", "proj-1")]);
    registerProjectsStore(projects);

    const client = stubClient({
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess",
        workflow_id: "plan",
        workflow_version: "1",
        revision: 1,
        status: "running",
        current_phase: "research",
        created_at: "t",
        updated_at: "t",
      }),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([
        { id: "plan", version: "1", name: "Plan" },
      ]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await refreshWorkflowState(appStore, client, "sess", "/tmp/p", [wireProject("/tmp/p", "proj-1")]);

    expect(client.listBlueprints).toHaveBeenCalledWith("proj-1");
    expect(appStore.state.activeWorkflowRun?.id).toBe("run-1");
    expect(appStore.state.workflowCatalog).toHaveLength(1);
  });
});
