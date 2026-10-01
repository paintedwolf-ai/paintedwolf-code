import { describe, expect, it } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import {
  isAnySessionActivityLive,
  isChatActivityLive,
} from "./session-activity.ts";

describe("session-activity", () => {
  it("is live when session busy or workers running", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "p1",
      workspace_path: "/tmp",
      status: "busy",
      posture: "build",
      created_at: "",
      activity_at: "",
      updated_at: "",
    });
    expect(isChatActivityLive(appStore, "s1")).toBe(true);

    appStore.actions.setCurrentSession({
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "p1",
      workspace_path: "/tmp",
      status: "idle",
      posture: "build",
      created_at: "",
      activity_at: "",
      updated_at: "",
    });
    appStore.actions.setWorkers([
      {
        id: "w1",
        parent_session_id: "s1",
        project_id: "p1",
        workspace_path: "/tmp",
        agent_type: "implementer",
        status: "running",
        execution_target: "local",
        created_at: "",
      },
    ]);
    expect(isChatActivityLive(appStore, "s1")).toBe(true);
  });

  it("isChatActivityLive ignores ambient workflow run when idle", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "p1",
      workspace_path: "/tmp",
      status: "idle",
      posture: "build",
      created_at: "",
      activity_at: "",
      updated_at: "",
    });
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
    expect(isChatActivityLive(appStore, "s1")).toBe(false);
  });

  it("isChatActivityLive is true while an llm call is active", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    expect(isChatActivityLive(appStore, "s1")).toBe(true);
  });

  it("a background session's llm turn never marks the viewed session live", () => {
    const appStore = createAppStore();
    // Viewing s1; s2 is running in the background on the same project stream.
    appStore.actions.setCurrentSession({
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s2",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    expect(isChatActivityLive(appStore, "s1")).toBe(false);
    expect(isChatActivityLive(appStore, "s2")).toBe(true);
    expect(isAnySessionActivityLive(appStore)).toBe(true);
  });

  it("host activity keeps its own session live before the provider call", () => {
    const appStore = createAppStore();
    appStore.actions.setActivity({
      activity_id: "activity-1",
      session_id: "s2",
      kind: "preparing_context",
      status: "active",
      started_at: "2026-01-01T00:00:00Z",
    });

    expect(isChatActivityLive(appStore, "s1")).toBe(false);
    expect(isChatActivityLive(appStore, "s2")).toBe(true);
    expect(isAnySessionActivityLive(appStore)).toBe(true);
  });

  it("keeps a background session's turn across a session switch", () => {
    const appStore = createAppStore();
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s2",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    appStore.actions.beginSessionResumeSwitch("s1");

    expect(isChatActivityLive(appStore, "s2")).toBe(true);
  });
});
