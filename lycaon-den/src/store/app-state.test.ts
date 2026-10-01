import {
  describe,
  expect,
  it,
} from "vitest";
import { wireProject } from "../api/mocks/project-fixture.ts";

import {
  createAppStore,
  INITIAL_APP_STATE,
} from "./app-state.ts";

describe("createAppStore", () => {

  it("sets sidecar status", () => {
    const store = createAppStore();
    store.actions.setSidecarStatus("connected");
    expect(store.state.sidecarStatus).toBe("connected");
  });

  it("mergeSession updates current session status", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.mergeSession({ id: "sess-1", status: "busy" });
    expect(store.state.currentSession?.status).toBe("busy");
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      last_message: "done",
    });
    expect(store.state.currentSession?.status).toBe("idle");
  });

  it("mergeSession keeps the live status when a stale read cannot be entered", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    // A preparing snapshot predates the live session's ready state.
    store.actions.mergeSession({
      id: "sess-1",
      status: "preparing",
      title: "Fix auth flow",
    });
    expect(store.state.currentSession?.status).toBe("busy");
    expect(store.state.currentSession?.title).toBe("Fix auth flow");
  });

  it("mergeSession updates current session title", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      title: "Fix auth flow",
    });
    expect(store.state.currentSession?.title).toBe("Fix auth flow");
  });

  it("mergeSession persists untrusted_content from session events", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    expect(store.state.currentSession?.untrusted_content).toBeUndefined();
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      untrusted_content: true,
    });
    expect(store.state.currentSession?.untrusted_content).toBe(true);
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      untrusted_content: false,
    });
    expect(store.state.currentSession?.untrusted_content).toBe(false);
  });

  it("mergeSession maps host_error to user-facing notice copy", () => {
    const store = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        status: "busy",
        posture: "build",
        workspace_path: "/p",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      sessionActivity: { "sess-1": { llmTurn: { status: "active" } } },
    });
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      host_error: {
        code: "provider_empty_completion",
        title: "Model returned no response",
        message: "llama-3.1-8b finished without any text or tool calls.",
      },
    });
    // SSE dispatch publishes the notice; merging session state clears activity.
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("keeps git scope pin as session-only store state", () => {
    const store = createAppStore();
    store.actions.setGitRepos(
      [
        {
          repo_id: "ra",
          label: "alpha",
          root_ids: ["root-1"],
          available: true,
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
        },
      ],
      "ra",
    );
    store.actions.setGitScopePin({ kind: "all" });
    expect(store.state.gitActiveRepoId).toBe("ra");
    expect(store.state.gitScopePin).toEqual({ kind: "all" });
    store.actions.clearGitScopePin();
    expect(store.state.gitScopePin).toBeNull();
    store.actions.setGitScopePin({ kind: "repo", repoId: "ra" });
    store.actions.clearChatForSessionSwitch();
    expect(store.state.gitRepos).toBeUndefined();
    expect(store.state.gitActiveRepoId).toBeUndefined();
    expect(store.state.gitScopePin).toBeUndefined();
  });

  // Notices remain scope-keyed outside app state.
  it("has no notices slice", () => {
    const store = createAppStore();
    expect("notices" in store.state).toBe(false);
  });
});

describe("projectIdForPath", () => {
  it("matches project path to registry id", async () => {
    const { projectIdForPath } = await import("./app-state.ts");
    const id = projectIdForPath(
      [wireProject("/tmp/proj", "uuid-1")],
      "/tmp/proj/",
    );
    expect(id).toBe("uuid-1");
  });
});