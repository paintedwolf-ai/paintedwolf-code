import { bindAppConnection } from "../../platform/connection/app-connection.ts";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { resetSessionSnapshotReads } from "../../api/session-snapshot-refresh.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { selectSessionNotices } from "../../notices/notice-select.ts";
import { stopChatActivity } from "./session-lifecycle.ts";
import type { Session } from "../../api/types.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { testProjectsStore } from "./session-lifecycle-test-fixture.ts";

describe("stopChatActivity", () => {
  const idleSession = (id: string, projectDir: string) => ({
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "proj-1",
    workspace_path: projectDir,
    status: "idle" as const,
    posture: "build" as const,
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });
  const busySession = (id: string, projectDir: string) => ({
    ...idleSession(id, projectDir),
    status: "busy" as const,
  });

  it("does not start old-backend follow-ups or clear a replacement stop attempt", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    let answer!: (session: Session) => void;
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({ abortSession: () => new Promise<Session>((resolve) => { answer = resolve; }), listWorkers });
    const stopping = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    resetSessionSnapshotReads(appStore.actions);
    appStore.actions.setSessionStopping("s1", true);
    answer(idleSession("s1", "/tmp/p"));
    await stopping;
    expect(listWorkers).not.toHaveBeenCalled();
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBe(true);
    expect(appStore.state.currentSession?.status).toBe("busy");
  });

  it("preserves a reopened session while finishing an earlier abort", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    let resolveAbort!: (session: Session) => void;
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      abortSession: () => new Promise<Session>((resolve) => { resolveAbort = resolve; }),
      listWorkers,
    });
    const aborting = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    appStore.actions.setCurrentSession(busySession("s2", "/tmp/p"));
    appStore.actions.setCurrentSession({ ...busySession("s1", "/tmp/p"), title: "Reopened" });
    resolveAbort(idleSession("s1", "/tmp/p"));
    await aborting;
    expect(appStore.state.currentSession).toMatchObject({ status: "busy", title: "Reopened" });
    expect(listWorkers).toHaveBeenCalledOnce();
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("posts abort, merges returned session, and clears activity flags", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    appStore.actions.holdPromptSubmission("s1", "submission-1");

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({ abortSession: abort, listWorkers });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    expect(appStore.state.currentSession?.status).toBe("idle");
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("does not merge the returned session when the foreground has switched", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("other", "/tmp/p"));

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    // Superseded hydration leaves the current session intact.
    expect(appStore.state.currentSession?.id).toBe("other");
    expect(appStore.state.currentSession?.status).toBe("busy");
  });

  it("with null client preserves truthful local activity", async () => {
    const appStore = createAppStore();
    appStore.actions.holdPromptSubmission("s1", "submission-1");
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    await stopChatActivity(appStore, null, "s1", "p1", "/tmp/p", emptyProjects);

    expect(appStore.state.sessionActivity["s1"]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
    expect(appStore.state.sessionActivity["s1"]?.llmTurn?.status).toBe("active");
  });

  it("surfaces abort errors and restores the live stop state", async () => {
    const appStore = createAppStore();
    const notices = createNoticeStore();
    bindAppConnection(connectionServicesFixture({ notices: notices }));
    appStore.actions.holdPromptSubmission("s1", "submission-1");

    const abort = vi.fn().mockRejectedValue(new Error("network down"));
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    // Scoped to the chat that failed to stop, not to whichever chat is in front.
    expect(selectSessionNotices(notices.index(), "s1")).toHaveLength(1);
    expect(appStore.state.sessionActivity["s1"]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBeUndefined();
  });

  it("refreshes session workers after a successful abort", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    const projectRows = [wireProject("/tmp/p", "proj-1")];
    bindAppConnection(connectionServicesFixture({ projects: testProjectsStore(projectRows) }));

    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      abortSession: vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p")),
      listWorkers,
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", projectRows);

    expect(listWorkers).toHaveBeenCalledWith("proj-1", { sessionId: "s1" });
  });

  it("a disconnected stop does not clear llm turn chrome", async () => {
    const appStore = createAppStore();
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    await stopChatActivity(appStore, null, "s1", "p1", "/tmp/p", emptyProjects);

    expect(appStore.state.sessionActivity["s1"]?.llmTurn?.status).toBe("active");
  });

  it("uses the single host abort operation for active catalog workflows", async () => {
    const appStore = createAppStore();
    bindAppConnection(connectionServicesFixture({ projects: testProjectsStore([wireProject("/tmp/p", "proj-1")]) }));
    appStore.actions.setCurrentSession(idleSession("s1", "/tmp/p"));
    appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: {
        id: "run-plan",
        session_id: "s1",
        workflow_id: "plan",
        workflow_version: "1.0.0",
		revision: 1,
        status: "running",
        current_phase: "plan",
        created_at: "t",
        updated_at: "t",
      },
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
    });

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const cancelWorkflowRun = vi.fn().mockResolvedValue({
      id: "run-plan",
      session_id: "s1",
      workflow_id: "plan",
      workflow_version: "1.0.0",
	  revision: 1,
      status: "cancelled",
      current_phase: "plan",
      created_at: "t",
      updated_at: "t",
    });
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
      cancelWorkflowRun,
      getActiveWorkflowRun: vi.fn().mockResolvedValue(undefined),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
		expect(cancelWorkflowRun).not.toHaveBeenCalled();
  });

  it("coalesces repeat stop clicks while the host barrier is running", async () => {
    const appStore = createAppStore();
    appStore.actions.holdPromptSubmission("s1", "submission-1");
    let resolveAbort!: (session: Session) => void;
    const abortSession = vi.fn(() => new Promise<Session>((resolve) => {
      resolveAbort = resolve;
    }));
    const client = stubClient({
      abortSession,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    const first = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBe(true);
    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    expect(abortSession).toHaveBeenCalledTimes(1);

    resolveAbort(idleSession("s1", "/tmp/p"));
    await expect(first).resolves.toBeUndefined();
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("does not cancel the ambient implement workflow on stop", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idleSession("s1", "/tmp/p"));
    appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: {
        id: "run-ambient",
        session_id: "s1",
        workflow_id: "implement",
        workflow_version: "1.0.0",
		revision: 1,
        attach_policy: "session_create",
        status: "running",
        current_phase: "work",
        created_at: "t",
        updated_at: "t",
      },
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
    });

    const cancelWorkflowRun = vi.fn();
    const client = stubClient({
      abortSession: vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p")),
      listWorkers: vi.fn().mockResolvedValue([]),
      cancelWorkflowRun,
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(cancelWorkflowRun).not.toHaveBeenCalled();
  });

});
