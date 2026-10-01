import { resetSessionSnapshotReads } from "../../api/session-snapshot-refresh.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { registerProjectsStore } from "../../platform/connection/app-connection.ts";
import type { Message, WorkflowRun } from "../../api/types.ts";
import { mergeWorkflowRunsFromMessages, refreshWorkflowState, startWorkflowForSession, exitActiveWorkflow } from "./workflow-actions.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { applySessionTranscriptSnapshot } from "../session/session-transcript-hydrate.ts";

const terminalRun: WorkflowRun = {
  id: "run-old",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
  revision: 1,
  attach_policy: "session_create",
  status: "complete",
  current_phase: "boot",
  created_at: "t",
  updated_at: "t",
};

describe("mergeWorkflowRunsFromMessages", () => {
  it("returns list unchanged when leaf already resolves", async () => {
    const runs = [terminalRun];
    const client = stubClient({ getWorkflowRun: vi.fn() });
    const out = await mergeWorkflowRunsFromMessages(
      client,
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: "run-old", created_at: "t" }],
      runs,
    );
    expect(out).toEqual(runs);
    expect(client.getWorkflowRun).not.toHaveBeenCalled();
  });

  it("fetches runs referenced on messages when list/active omit them", async () => {
    const client = stubClient({
      getWorkflowRun: vi.fn().mockResolvedValue(terminalRun),
    });
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: "run-old", created_at: "t" },
    ];
    const out = await mergeWorkflowRunsFromMessages(client, messages, []);
    expect(out).toEqual([terminalRun]);
    expect(client.getWorkflowRun).toHaveBeenCalledWith("run-old");
  });

  it("hydrates missing transcript runs even when an active leaf resolves", async () => {
    const referenced = { ...terminalRun, id: "run-referenced" };
    const client = stubClient({
      getWorkflowRun: vi.fn().mockResolvedValue(referenced),
    });
    const messages: Message[] = [
      {
        id: "m1",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "hi",
        workflow_run_id: referenced.id,
        created_at: "t",
      },
    ];
    const out = await mergeWorkflowRunsFromMessages(client, messages, [terminalRun]);
    expect(out.map((run) => run.id)).toContain(referenced.id);
    expect(client.getWorkflowRun).toHaveBeenCalledWith(referenced.id);
  });
});

describe("refreshWorkflowState", () => {
  it("does not let a background follow-up supersede the foreground refresh", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "b", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    let finish!: (run: WorkflowRun) => void;
    const active = new Promise<WorkflowRun>((resolve) => { finish = resolve; });
    const options = { catalog: false, history: false, blueprints: false };
    const foreground = refreshWorkflowState(store, stubClient({ getActiveWorkflowRun: () => active }), "b", "/project", [], options);
    await refreshWorkflowState(store, stubClient({ getActiveWorkflowRun: async () => undefined }), "a", "/project", [], options);
    finish({ ...terminalRun, session_id: "b", id: "foreground" });
    await foreground;
    expect(store.state.activeWorkflowRun?.id).toBe("foreground");
  });

  it("keeps streamed run state while a transcript baseline hydrates", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    const oldRun: WorkflowRun = { ...terminalRun, status: "running" };
    store.actions.setWorkflowState("s1", store.state.sessionViewEpoch, {
      activeWorkflowRun: oldRun, workflowRuns: [oldRun], workflowCatalog: [], blueprints: [],
    });
    let finish!: (run: WorkflowRun) => void;
    const active = new Promise<WorkflowRun>((resolve) => { finish = resolve; });
    const hydrating = applySessionTranscriptSnapshot(store, stubClient({
      getActiveWorkflowRun: () => active,
      listSessionWorkflowRuns: async () => ({ runs: [oldRun] }),
      listWorkflows: async () => [],
    }), "s1", "/project", { messages: [], watermark: 1, turn_clocks: {}, turn_loads: {} }, []);
    store.actions.applyWorkflowRunEvent({ run: { ...oldRun, revision: 2, status: "paused" } });
    finish(oldRun);
    await hydrating;
    expect(store.state.activeWorkflowRun).toMatchObject({ revision: 2, status: "paused" });
    expect(store.state.transcriptWatermark).toBe(1);
  });

  it("keeps a newer streamed run when an earlier refresh completes", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    });
    const oldRun: WorkflowRun = { ...terminalRun, status: "running" };
    store.actions.setWorkflowState("s1", store.state.sessionViewEpoch, {
      activeWorkflowRun: oldRun, workflowRuns: [oldRun], workflowCatalog: [], blueprints: [],
    });
    let finish!: (run: WorkflowRun) => void;
    const active = new Promise<WorkflowRun>((resolve) => { finish = resolve; });
    const refreshing = refreshWorkflowState(store, stubClient({ getActiveWorkflowRun: () => active }),
      "s1", "/project", [], { catalog: false, history: false, blueprints: false });
    const updated = { ...oldRun, revision: 2, status: "paused" as const };
    store.actions.applyWorkflowRunEvent({ run: updated });
    finish(oldRun);
    await refreshing;
    expect(store.state.activeWorkflowRun).toMatchObject({ revision: 2, status: "paused" });
    expect(store.state.workflowRuns[0]).toMatchObject({ revision: 2, status: "paused" });
  });

  it("loads active run without catalog prefetch", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
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
        session_id: "sess-1",
        workflow_id: "implement",
        workflow_version: "1.0.0",
        revision: 1,
        attach_policy: "session_create",
        status: "running",
        current_phase: "boot",
        created_at: "t",
        updated_at: "t",
      }),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await refreshWorkflowState(appStore, client, "sess-1", "/tmp/p", emptyProjects, {
      catalog: false,
      history: false,
      blueprints: false,
    });
    expect(client.getActiveWorkflowRun).toHaveBeenCalledTimes(1);
    expect(appStore.state.activeWorkflowRun?.id).toBe("run-1");
  });
});

describe("startWorkflowForSession", () => {
  it("binds replacement to the run revision the UI reviewed", async () => {
    const appStore = createAppStore();
    const active = {
      ...terminalRun,
      id: "run-active",
      status: "running" as const,
      revision: 4,
    };
    appStore.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: active,
      workflowRuns: [active],
      workflowCatalog: [],
      blueprints: [],
    });
    const startWorkflowRun = vi.fn().mockResolvedValue(active);
    const client = stubClient({
      startWorkflowRun,
      getActiveWorkflowRun: vi.fn().mockResolvedValue(active),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [active] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await startWorkflowForSession(appStore, client, "s1", "/tmp/p", emptyProjects, {
      workflow_id: "plan",
      workflow_version: "1.0.0",
    });

    expect(startWorkflowRun).toHaveBeenCalledWith("s1", {
      operation_id: expect.any(String),
      workflow_id: "plan",
      workflow_version: "1.0.0",
      replace_run_id: "run-active",
      expected_revision: 4,
    });
  });
});


describe("workflow mutation backend changes", () => {
  it.each(["start", "exit"] as const)("does not refresh a replacement backend after %s settles", async (action) => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    let finish!: (run: WorkflowRun) => void;
    const mutation = new Promise<WorkflowRun>((resolve) => { finish = resolve; });
    const client = stubClient({
      startWorkflowRun: vi.fn(() => mutation),
      exitWorkflowRun: vi.fn(() => mutation),
      getActiveWorkflowRun: vi.fn(),
    });
    const pending = action === "start"
      ? startWorkflowForSession(store, client, "s1", "/project", [], { workflow_id: "implement", workflow_version: "1.0.0" })
      : exitActiveWorkflow(store, client, "s1", "/project", [], undefined, terminalRun);
    resetSessionSnapshotReads(store.actions);
    finish(terminalRun);
    await expect(pending).resolves.toEqual(terminalRun);
    expect(client.getActiveWorkflowRun).not.toHaveBeenCalled();
  });
});
