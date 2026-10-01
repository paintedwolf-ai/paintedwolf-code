import {
  describe,
  expect,
  it,
} from "vitest";

import {
  clearTranscriptEntryMemory,
  hasTranscriptEntryMemory,
  transcriptEntryKeysFromMessages,
} from "../chat/transcript/presentation/transcript-entry.ts";
import { createAppStore } from "./app-state.ts";

describe("createAppStore", () => {

  it("clearChatForSessionSwitch clears transcript and side state", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-old",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.installTranscriptBaseline(
      "sess-old",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "old", created_at: "t" }],
      0,
    );
    store.actions.applyWorkerTranscriptRows("w1", [
      { id: "c1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" },
    ]);
    store.actions.setBoard({
      summary: "old board",
      session_id: "sess-old",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "old",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
    });
    store.actions.clearChatForSessionSwitch("sess-1");
    expect(store.state.messages).toEqual([]);
    expect(store.state.currentSession).toBeUndefined();
    expect(store.state.board).toBeUndefined();
    expect(store.state.workerTranscripts).toEqual({});
    expect(store.state.chatHydrationLock).toBe("sess-1");
    store.actions.completeChatSessionHydration();
    expect(store.state.chatHydrationLock).toBeUndefined();
  });

  it("beginSessionResumeSwitch keeps the transcript and companion slices visible", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-old",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "old", created_at: "t" }],
      0,
    );
    store.actions.applyWorkerTranscriptRows("w1", [
      { id: "c1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" },
    ]);
    store.actions.setBoard({
      summary: "old board",
      session_id: "sess-old",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "old",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
    });
    store.actions.beginSessionResumeSwitch("sess-1");
    expect(store.state.messages).toHaveLength(1);
    expect(store.state.workerTranscripts.w1?.rows).toEqual([
      { id: "c1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" },
    ]);
    expect(store.state.board?.summary).toBe("old board");
    expect(store.state.chatHydrationLock).toBe("sess-1");
    store.actions.completeChatSessionHydration();
    expect(store.state.chatHydrationLock).toBeUndefined();
  });

  it("installTranscriptBaseline baselines entry fade only on fresh hydration", () => {
    const sessionId = "sess-fade";
    clearTranscriptEntryMemory(sessionId);
    const store = createAppStore();

    const history = [
      { id: "m1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 1 },
    ];
    // Loaded history has already entered the transcript.
    store.actions.installTranscriptBaseline(sessionId, history, 1);
    for (const key of transcriptEntryKeysFromMessages(history)) {
      expect(hasTranscriptEntryMemory(sessionId, key)).toBe(true);
    }

    // Newly discovered tail rows retain their entry fade.
    const withTail = [
      ...history,
      { id: "a1", role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "reply", created_at: "t", seq: 2 },
    ];
    store.actions.installTranscriptBaseline(sessionId, withTail, 2);
    const historyKeys = new Set(transcriptEntryKeysFromMessages(history));
    const tailKeys = transcriptEntryKeysFromMessages(withTail).filter(
      (key) => !historyKeys.has(key),
    );
    expect(tailKeys.length).toBeGreaterThan(0);
    for (const key of tailKeys) {
      expect(hasTranscriptEntryMemory(sessionId, key)).toBe(false);
    }
  });

  it("resetChatForSessionSwitch clears record-typed session slices", () => {
    const store = createAppStore();
    store.actions.holdPromptSubmission("sess-1", "submission-1");
    store.actions.applyWorkerTranscriptRows("worker-1", [
      {
        id: "m1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "hi",
        created_at: "t",
      },
    ]);
    store.actions.addPendingSend("sess-1", {
      kind: "prompt",
      operationId: "op-1",
      text: "hi",
      state: "sending",
    });
    expect(store.state.sessionActivity["sess-1"]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
    expect(store.state.workerTranscripts["worker-1"]?.rows).toHaveLength(1);

    store.actions.resetChatForSessionSwitch();

    expect(store.state.sessionActivity).toEqual({});
    expect(store.state.workerTranscripts).toEqual({});
    expect(store.state.pendingSends).toEqual({});
  });

  it("resetChatForSessionSwitch clears the transcript", () => {
    const store = createAppStore();
    store.actions.upsertMessage({
      id: "m1",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "hi",
      created_at: "t",
      seq: 1,
      ord: 1,
    });
    expect(store.state.messages).toHaveLength(1);
    store.actions.resetChatForSessionSwitch();
    expect(store.state.messages).toHaveLength(0);
  });

  it("upsertMessage rejects a patch older than the row it would overwrite", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [{ id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "final", created_at: "t", seq: 5 }],
      5,
    );
    const applied = store.actions.upsertMessage({
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "stale streaming chunk",
      created_at: "t",
      seq: 3,
    });
    expect(applied).toBe(false);
    expect(store.state.messages[0]?.content).toBe("final");
  });

  it("upsertMessage adopts a newer snapshot wholesale — absent fields clear", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "The web-researcher leg failed.",
          draft_status: "live",
          created_at: "t",
          seq: 5,
          tool_calls: [{ id: "call_progress", name: "update_progress", args: {} }],
        },
      ],
      5,
    );
    // The committed snapshot omits the unexecuted call.
    const applied = store.actions.upsertMessage({
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "The web-researcher leg failed.",
      kind: "draft",
      draft_status: "committed",
      created_at: "t",
      seq: 6,
    });
    expect(applied).toBe(true);
    expect(store.state.messages[0]?.tool_calls).toBeUndefined();
    expect(store.state.messages[0]?.draft_status).toBe("committed");
  });

  it("upsertMessage patches a live row without replacing sibling identities", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        {
          id: "u1",
          role: "user",
          origin: "user" as const,
          authority: "user" as const,
          trust_tier: "trusted" as const,
          content: "hi",
          created_at: "t",
          seq: 1,
          ord: 1,
        },
        {
          id: "a1",
          role: "assistant",
          origin: "model" as const,
          authority: "none" as const,
          trust_tier: "trusted" as const,
          content: "",
          created_at: "t",
          seq: 2,
          ord: 2,
          draft_status: "live",
        },
        {
          id: "t1",
          role: "tool",
          origin: "tool" as const,
          authority: "none" as const,
          trust_tier: "trusted" as const,
          content: "tool body",
          created_at: "t",
          seq: 3,
          ord: 3,
        },
      ],
      3,
    );
    const userBefore = store.state.messages[0];
    const toolBefore = store.state.messages[2];
    const listBefore = store.state.messages;
    for (let i = 1; i <= 20; i++) {
      expect(
        store.actions.upsertMessage({
          id: "a1",
          role: "assistant",
          origin: "model" as const,
          authority: "none" as const,
          trust_tier: "trusted" as const,
          content: "token-".repeat(i),
          created_at: "t",
          draft_status: "live",
          status: "streaming",
        }),
      ).toBe(true);
    }
    expect(store.state.messages).toBe(listBefore);
    expect(store.state.messages[0]).toBe(userBefore);
    expect(store.state.messages[2]).toBe(toolBefore);
    expect(store.state.messages[1]?.content).toBe("token-".repeat(20));
  });

  it("upsertMessage applies a patch newer than the held row", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [{ id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "streaming", created_at: "t", seq: 2 }],
      2,
    );
    const applied = store.actions.upsertMessage({
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "committed",
      created_at: "t",
      seq: 7,
    });
    expect(applied).toBe(true);
    expect(store.state.messages[0]?.content).toBe("committed");
  });

  it("upsertMessage drops a patch to a non-resident older Ord", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 50, ord: 50 },
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "tip",
          created_at: "t",
          seq: 51,
          ord: 51,
        },
      ],
      51,
      { hasMoreBefore: true },
    );
    const before = store.state.messages.map((m) => m.id);
    const applied = store.actions.upsertMessage({
      id: "ancient",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "patched mid-history",
      created_at: "t",
      seq: 99,
      ord: 3,
    });
    expect(applied).toBe(false);
    expect(store.state.messages.map((m) => m.id)).toEqual(before);
    expect(store.state.messages[0]?.ord).toBe(50);
  });

  it("installTranscriptBaseline keeps a local row newer than the page watermark", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline("sess-1", [], 0);
    // An SSE row landed at seq 9 before the refetch completed.
    store.actions.upsertMessage({
      id: "a2",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "live",
      created_at: "t",
      seq: 9,
    });
    // The snapshot watermark predates the live row.
    store.actions.installTranscriptBaseline(
      "sess-1",
      [{ id: "a1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 1 }],
      3,
    );
    expect(store.state.messages.map((m) => m.id)).toContain("a2");
  });

  it("installTranscriptBaseline for a different session replaces wholesale", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [{ id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "old", created_at: "t", seq: 9 }],
      9,
    );
    store.actions.installTranscriptBaseline(
      "sess-2",
      [{ id: "b1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "new", created_at: "t", seq: 1 }],
      1,
    );
    expect(store.state.messages.map((m) => m.id)).toEqual(["b1"]);
  });

  it("installTranscriptBaseline skips store write when the merged page is unchanged", () => {
    const store = createAppStore();
    const baseline = [
      { id: "a1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 1 },
    ];
    store.actions.installTranscriptBaseline("sess-1", baseline, 1);
    const before = store.state.messages;
    store.actions.installTranscriptBaseline("sess-1", baseline, 1);
    expect(store.state.messages).toBe(before);
  });

  it("buffers parent message events during hydrate and replays on completion", () => {
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
    store.actions.clearChatForSessionSwitch("sess-1");
    store.actions.bufferHydrationMessageEvent({
      session_id: "sess-1",
      op: "append",
      seq: 4,
      message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "buffered", created_at: "t", seq: 4 },
    });
    const drained = store.actions.takeHydrationBuffer();
    expect(drained).toHaveLength(1);
    expect(store.actions.takeHydrationBuffer()).toHaveLength(0);
  });
});
