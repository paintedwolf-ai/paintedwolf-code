// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { subscribeEvents } from "../../../api/events.ts";
import type { EventEnvelopeBase, Message, WorkflowRun } from "../../../api/types.ts";
import { createAppStore } from "../../../store/app-state.ts";
import {
  buildChatTranscriptBlocks,
  isCatalogSpan,
} from "../../workflow/workflow-spans.ts";

const PROJECT_ID = "fcc9b4e9-d223-45f7-a964-ecc55fc87706";
const SESSION_ID = "b84cd49b-5dd6-49d8-9bad-957e96eda1e2";
const RUN_ID = "7f1e64b0-e4dc-508b-ab66-d490e7b31b21";

const ambientRun: WorkflowRun = {
  id: RUN_ID,
  session_id: SESSION_ID,
  project_id: PROJECT_ID,
  workflow_id: "implement",
  workflow_version: "1.0.0",
  attach_policy: "session_create",
  status: "running",
  revision: 1,
  current_phase: "boot",
  created_at: "2026-08-12T14:21:08.945Z",
  updated_at: "2026-08-12T14:21:08.945Z",
};

const bootBoundary: Message = {
  id: "5b3196fe-0688-574d-b8c9-3b9cdbc78edb",
  role: "system",
  origin: "host",
  authority: "system",
  trust_tier: "trusted",
  content: "",
  kind: "workflow_boundary",
  workflow_run_id: RUN_ID,
  visibility: "internal",
  seq: 1,
  ord: 1,
  created_at: "2026-08-12T14:21:08.909Z",
};

const firstPrompt: Message = {
  id: "4a90cd1b-93c4-481e-b93c-813e4cde3e4a",
  role: "user",
  origin: "user",
  authority: "user",
  trust_tier: "trusted",
  content: "Tell me about this repo",
  workflow_run_id: RUN_ID,
  visibility: "transcript",
  seq: 2,
  ord: 2,
  created_at: "2026-08-12T14:21:20.574Z",
};

const scope = {
  kind: "session",
  project_id: PROJECT_ID,
  session_id: SESSION_ID,
} as const;

function envelope(topic: EventEnvelopeBase["topic"], data: unknown): { data: string } {
  return {
    data: JSON.stringify({
      v: 1,
      event_id: crypto.randomUUID(),
      cursor: crypto.randomUUID(),
      topic,
      published_at: "2026-08-12T14:21:08.945Z",
      scope,
      data,
    } satisfies EventEnvelopeBase),
  };
}

function storeForSession() {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({
    id: SESSION_ID,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: PROJECT_ID,
    workspace_path: "/tmp/opencode",
    posture: "build",
    status: "busy",
    created_at: "2026-08-12T14:21:08.788Z",
    activity_at: "2026-08-12T14:21:08.788Z",
    updated_at: "2026-08-12T14:21:08.788Z",
  });
  appStore.actions.installTranscriptBaseline(SESSION_ID, [], 0);
  return appStore;
}

/** Exercises session-start ordering through the event subscription. */
describe("session start is atomic across the event stream", () => {
  async function replay(
    appStore: ReturnType<typeof createAppStore>,
    frames: Array<{ data: string }>,
  ) {
    async function* connect() {
      yield { comment: "connected" };
      for (const frame of frames) yield frame;
      await new Promise(() => {
        /* hold the stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      PROJECT_ID,
      {},
      {
        appStore,
        storeActions: appStore.actions,
        connect: () => connect(),
      },
    );
    await vi.waitFor(() => {
      expect(appStore.state.messages.length).toBe(2);
    });
    void sub.close();
    return sub;
  }

  it("installs the run before the rows that name it", async () => {
    const appStore = storeForSession();

    await replay(appStore, [
      envelope("workflow", {
        workflow_id: "implement",
        workflow_run_id: RUN_ID,
        run: ambientRun,
        phase: "boot",
        status: "running",
      }),
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 1,
        message: bootBoundary,
      }),
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 2,
        message: firstPrompt,
      }),
    ]);

    expect(appStore.state.workflowRuns.map((r) => r.id)).toEqual([RUN_ID]);
    const blocks = buildChatTranscriptBlocks(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.ambientSpan).toBe(true);
    expect(isCatalogSpan(blocks[0]!)).toBe(false);
    expect(
      appStore.state.messages.map((m) => m.content),
    ).toContain("Tell me about this repo");
  });

  it("renders the transcript even when the run never arrives", async () => {
    const appStore = storeForSession();

    await replay(appStore, [
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 1,
        message: bootBoundary,
      }),
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 2,
        message: firstPrompt,
      }),
    ]);

    expect(appStore.state.workflowRuns).toEqual([]);
    const blocks = buildChatTranscriptBlocks(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
    expect(blocks.flatMap((b) => b.items).length).toBeGreaterThan(0);
  });

  it("ignores a run belonging to another session", async () => {
    const appStore = storeForSession();

    await replay(appStore, [
      envelope("workflow", {
        workflow_id: "implement",
        workflow_run_id: "run-elsewhere",
        run: {
          ...ambientRun,
          id: "run-elsewhere",
          session_id: "some-other-session",
        },
        status: "running",
      }),
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 1,
        message: bootBoundary,
      }),
      envelope("message", {
        session_id: SESSION_ID,
        op: "append",
        seq: 2,
        message: firstPrompt,
      }),
    ]);

    expect(appStore.state.workflowRuns).toEqual([]);
  });

  it("keeps the newer run state when a slower refetch reports an older revision", () => {
    const appStore = storeForSession();
    const advanced: WorkflowRun = {
      ...ambientRun,
      revision: 4,
      current_phase: "work",
    };

    appStore.actions.applyWorkflowRunEvent({ run: advanced });
    appStore.actions.applyWorkflowRunEvent({ run: ambientRun });

    expect(appStore.state.workflowRuns[0]?.current_phase).toBe("work");
    expect(appStore.state.workflowRuns[0]?.revision).toBe(4);
  });
});
