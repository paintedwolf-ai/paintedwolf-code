import { beforeEach, describe, expect, it } from "vitest";
import { INITIAL_APP_STATE, createAppStore } from "../../../store/app-state.ts";
import { flushWorkerTranscriptCoalesce } from "../../worker/worker-transcript-coalesce.ts";
import { applyMessageEvent } from "./message-events.ts";
import {
  bufferChildMessageEvent,
  clearChildMessageBuffer,
  takeBufferedMessageEvents,
} from "./worker-child-message-buffer.ts";

describe("worker-child-message-buffer", () => {
  beforeEach(() => clearChildMessageBuffer());

  it("replays buffered child-session rows once the worker roster binds", () => {
    const appStore = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "parent-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      workers: [
        {
          id: "job-1",
          status: "running",
          agent_type: "implementer",
          parent_session_id: "parent-1",
          created_at: "t",
        },
      ],
    });

    bufferChildMessageEvent("child-1", {
      session_id: "child-1",
      op: "append",
      message: {
        id: "m1",
        worker_id: "job-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "asking",
        created_at: "t",
        seq: 1,
      },
    });

    appStore.actions.updateWorker({
      worker_id: "job-1",
      status: "running",
      child_session_id: "child-1",
      parent_session_id: "parent-1",
    });
    flushWorkerTranscriptCoalesce(appStore);

    expect(appStore.state.workerTranscripts["job-1"]?.rows?.map((m) => m.id)).toEqual([
      "m1",
    ]);

    expect(
      applyMessageEvent(appStore, {
        session_id: "child-1",
        op: "append",
        message: {
          id: "m2",
          worker_id: "job-1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "decision_requested",
          created_at: "t",
          seq: 2,
        },
      }),
    ).toBe(true);
    flushWorkerTranscriptCoalesce(appStore);
    expect(appStore.state.workerTranscripts["job-1"]?.rows?.map((m) => m.id)).toEqual([
      "m1",
      "m2",
    ]);
  });

  it("replays a non-worker session's buffered rows once it becomes the foreground", () => {
    const appStore = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "parent-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
    });

    // Foregrounding drains buffered coordinator rows.
    expect(
      applyMessageEvent(appStore, {
        session_id: "parent-2",
        op: "append",
        message: { id: "m1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 1 },
      }),
    ).toBe(false);
    expect(appStore.state.messages.map((m) => m.id)).toEqual([]);

    appStore.actions.setCurrentSession({
      id: "parent-2",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });

    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
  });

  it("coalesces repeated rows and bounds sessions and rows", () => {
    for (let i = 0; i < 520; i++) {
      bufferChildMessageEvent("kept", {
        session_id: "kept",
        op: "append",
        message: {
          id: `m-${i}`,
          role: "assistant",
          origin: "model",
          authority: "none",
          trust_tier: "trusted",
          content: `row ${i}`,
          created_at: "t",
          seq: i,
        },
      });
    }
    bufferChildMessageEvent("kept", {
      session_id: "kept",
      op: "patch",
      message: {
        id: "m-519",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "latest",
        created_at: "t",
        seq: 600,
      },
    });
    const rows = takeBufferedMessageEvents("kept");
    expect(rows).toHaveLength(512);
    expect(rows[rows.length - 1]?.message.content).toBe("latest");

    for (let i = 0; i < 65; i++) {
      bufferChildMessageEvent(`session-${i}`, {
        session_id: `session-${i}`,
        op: "append",
        message: {
          id: "row",
          role: "assistant",
          origin: "model",
          authority: "none",
          trust_tier: "trusted",
          content: "x",
          created_at: "t",
        },
      });
    }
    expect(takeBufferedMessageEvents("session-0")).toEqual([]);
    expect(takeBufferedMessageEvents("session-64")).toHaveLength(1);
  });
});
