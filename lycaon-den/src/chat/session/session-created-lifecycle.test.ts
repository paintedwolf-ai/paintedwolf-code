import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { refreshCreatedSessionBoard, recordCreatedSessionInRecents } from "./session-lifecycle.ts";
import { DEFAULT_SESSION_TITLE } from "../../../shared/app-state-types.ts";
import { testProjectsStore } from "./session-lifecycle-test-fixture.ts";

describe("session-lifecycle", () => {
  it("refreshCreatedSessionBoard refreshes project and board without repeating bootstrap workflow reads", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-new", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "proj-1", workspace_path: "/tmp/p",
      posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    });
    const projects = testProjectsStore([wireProject("/tmp/p")]);
    const listWorkflows = vi.fn().mockResolvedValue([]);
    const listRuns = vi.fn().mockResolvedValue({ runs: [] });
    const listBlueprints = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      listProjects: vi.fn().mockResolvedValue([
        wireProject("/tmp/p"),
      ]),
      getBoard: vi.fn().mockResolvedValue({ columns: [] }),
      listWorkers: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess-new",
        workflow_id: "implement",
        status: "running",
        current_phase: "boot",
        attach_policy: "session_create",
        created_at: "t",
        updated_at: "t",
      }),
      listSessionWorkflowRuns: listRuns,
      listWorkflows,
      listBlueprints,
    });

    await refreshCreatedSessionBoard(appStore, client, projects, "/tmp/p", "sess-new");

    expect(client.listProjects).toHaveBeenCalledTimes(1);
    expect(client.getBoard).toHaveBeenCalledWith("proj-1", "sess-new");
    expect(client.getActiveWorkflowRun).not.toHaveBeenCalled();
    expect(listWorkflows).not.toHaveBeenCalled();
    expect(listRuns).not.toHaveBeenCalled();
    expect(listBlueprints).not.toHaveBeenCalled();
  });

  it("recordCreatedSessionInRecents lists the new session first under its project", async () => {
    const recents = createRecentsStore();
    const projectId = "proj-1";
    for (let i = 0; i < 5; i++) {
      await recents.registerSession({
        projectId,
        sessionId: `sess-old-${i}`,
        title: `Old ${i}`,
      });
    }

    await recordCreatedSessionInRecents(recents, {
      projectId,
      sessionId: "sess-new",
    });

    const projectRecents = recents.state.recents.filter(
      (row) => row.projectId === projectId,
    );
    expect(projectRecents[0]?.sessionId).toBe("sess-new");
    expect(projectRecents[0]?.title).toBe(DEFAULT_SESSION_TITLE);
  });

});
