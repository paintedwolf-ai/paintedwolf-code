import { describe, expect, it } from "vitest";
import type { Message, WorkflowRun } from "../../../api/types.ts";
import type { LlmTurnActivity } from "../../../store/app-state-model.ts";
import { latestMessageSnapshot } from "./messages-equal.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import {
  buildChatTranscriptBlocks,
  type ChatSpanBlock,
} from "../../workflow/workflow-spans.ts";

/** Replays a guarded coordinator turn across durable draft slots. */

const ambientRun: WorkflowRun = {
  id: "run-ambient",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "boot",
  start_message_id: "m-user",
  created_at: "t",
  updated_at: "t",
};

const SLOT = "slot-1";

const activeTurn: LlmTurnActivity = {
  status: "active",
  guarded: true,
};

type Store = { messages: Message[] };

function apply(store: Store, patch: Message): void {
  const idx = store.messages.findIndex((m) => m.id === patch.id);
  if (idx < 0) {
    store.messages = [...store.messages, patch];
    return;
  }
  store.messages = store.messages.map((m, i) =>
    i === idx ? latestMessageSnapshot(m, patch) : m,
  );
}

function displayed(
  store: Store,
  llmTurnActivity: LlmTurnActivity | undefined,
): { items: TranscriptItem[]; blocks: ChatSpanBlock[] } {
  const messages = store.messages.map((m) => ({
    ...m,
    workflow_run_id: m.workflow_run_id ?? ambientRun.id,
    status:
      llmTurnActivity?.status === "active" && m.id === SLOT
        ? ("streaming" as const)
        : (m.status ?? "complete"),
  }));
  const blocks = buildChatTranscriptBlocks(messages, [ambientRun], ambientRun, {
    verboseMode: true,
  });
  const block = blocks[blocks.length - 1]!;
  const items = createTranscriptDisplayProjector()(messages, [block.items], { verboseMode: true })[0]!;
  return { items, blocks };
}

function toolItemByKey(
  items: readonly TranscriptItem[],
  key: string,
): Extract<TranscriptItem, { kind: "tool" }> | undefined {
  for (const item of items) {
    if (item.kind === "tool" && item.key === key) return item;
    if (item.kind === "worker_group") {
      const part = item.parts.find((candidate) => candidate.id === key);
      if (part) return { kind: "tool", key, part };
    }
    if (item.kind === "activity_span") {
      for (const entry of item.entries) {
        if (entry.kind === "tool" && entry.part.id === key) {
          return { kind: "tool", key, part: entry.part };
        }
      }
    }
  }
  return undefined;
}

function toolItemsByName(
  items: readonly TranscriptItem[],
  tool: string,
): Array<Extract<TranscriptItem, { kind: "tool" }>> {
  return items.flatMap((item) => {
    if (item.kind === "tool") {
      return item.part.tool === tool ? [item] : [];
    }
    if (item.kind === "worker_group") {
      return item.parts.flatMap((part) =>
        part.tool === tool
          ? [{ kind: "tool" as const, key: part.id, part }]
          : [],
      );
    }
    if (item.kind !== "activity_span") return [];
    return item.entries.flatMap((entry) =>
      entry.kind === "tool" && entry.part.tool === tool
        ? [{ kind: "tool" as const, key: entry.part.id, part: entry.part }]
        : [],
    );
  });
}

function proseRow(
  items: readonly TranscriptItem[],
): Extract<TranscriptItem, { kind: "draft" | "assistant" }> | undefined {
  return items.find(
    (item): item is Extract<TranscriptItem, { kind: "draft" | "assistant" }> =>
      (item.kind === "draft" || item.kind === "assistant") && item.key === SLOT,
  );
}

function toolRow(
  id: string,
  toolCallId: string,
  tool: string,
  content: string,
  ts: string,
  assistantMessageId = SLOT,
  toolArgs: Record<string, unknown> = {},
): Message {
  return {
    id,
    role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
    content,
    created_at: ts,
    tool_result: {
      content,
      tool,
      tool_call_id: toolCallId,
      assistant_message_id: assistantMessageId,
      tool_args: toolArgs,
      outcome: "completed",
    },
  } as Message;
}

describe("guarded coordinator turn replay", () => {
  it("keeps completed tool chips across per-step draft slots and supersession", () => {
    const store: Store = { messages: [] };
    const see = () => displayed(store, activeTurn).items;
    const STEP1 = "slot-step-1";
    const STEP2 = "slot-step-2";
    const ANSWER = SLOT;

    apply(store, {
      id: "m-user",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "Tell me about this repo",
      created_at: "2026-07-07T22:25:26Z",
    } as Message);

    // Mid-run step 1: list_dir + git_status — committed, slot closed (tool chips only).
    apply(store, {
      id: STEP1,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      visibility: "transcript",
      draft_status: "committed",
      created_at: "2026-07-07T22:25:28Z",
      tool_calls: [
        { id: "call_1", name: "list_dir", args: { path: "." } },
        { id: "call_2", name: "git_status", args: {} },
      ],
    } as Message);
    apply(store, toolRow("t1", "call_1", "list_dir", "[list#1]\n{}", "2026-07-07T22:25:29Z", STEP1, { path: "." }));
    apply(store, toolRow("t2", "call_2", "git_status", "[git#1]\n{}", "2026-07-07T22:25:29Z", STEP1, {}));

    let items = see();
    const listChipKey = `${STEP1}:call_1`;
    expect(toolItemByKey(items, listChipKey)?.part.tool).toBe("list_dir");

    // Mid-run step 2 on a fresh slot: read×3 (authoritative batch — no union with step 1).
    apply(store, {
      id: STEP2,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      visibility: "transcript",
      draft_status: "committed",
      created_at: "2026-07-07T22:25:30Z",
      tool_calls: [
        { id: "call_3", name: "read", args: { path: "README.md" } },
        { id: "call_4", name: "read", args: { path: "AGENTS.md" } },
        { id: "call_5", name: "read", args: { path: "interfaces.md" } },
      ],
    } as Message);
    apply(store, toolRow("t3", "call_3", "read", "[read#1]\n{}", "2026-07-07T22:25:34Z", STEP2, { path: "README.md" }));
    apply(store, toolRow("t4", "call_4", "read", "[read#2]\n{}", "2026-07-07T22:25:34Z", STEP2, { path: "AGENTS.md" }));
    apply(store, toolRow("t5", "call_5", "read", "[read#3]\n{}", "2026-07-07T22:25:34Z", STEP2, { path: "interfaces.md" }));

    items = see();
    expect(toolItemByKey(items, listChipKey)?.part.tool).toBe("list_dir");
    expect(toolItemByKey(items, listChipKey)?.part.args).toEqual({ path: "." });
    expect(toolItemByKey(items, `${STEP2}:call_3`)?.part.tool).toBe("read");

    // Prose-only slot updates retain the preceding tool steps.
    apply(store, {
      id: ANSWER,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "This is **Painted Wolf Code / Lycaon** — a local-first AI coding agent.",
      visibility: "internal",
      draft_status: "live",
      created_at: "2026-07-07T22:25:42Z",
    } as Message);

    items = see();
    let prose = proseRow(items);
    expect(prose?.kind).toBe("draft");
    expect(prose && "live" in prose && prose.live).toBe(true);
    expect(toolItemByKey(items, listChipKey)?.part.tool).toBe("list_dir");

    // Citation-grounding reject: the host keeps the rejected body on the wire
    // row and stamps the grown version count (supersession, not a wipe).
    apply(store, {
      id: ANSWER,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "This is **Painted Wolf Code / Lycaon** — a local-first AI coding agent.",
      visibility: "internal",
      draft_status: "live",
      draft_version_count: 2,
      created_at: "2026-07-07T22:25:49Z",
    } as Message);

    items = see();
    prose = proseRow(items);
    expect(prose?.kind).toBe("draft");
    expect(prose?.text).toContain("Painted Wolf");

    // Retry streams the closeout JSON on the same answer slot.
    apply(store, {
      id: ANSWER,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: '```json\n{ "synthesis": "## Repo overview',
      visibility: "internal",
      draft_status: "live",
      created_at: "2026-07-07T22:25:54Z",
    } as Message);

    items = see();
    expect(proseRow(items)?.kind).toBe("draft");

    // Final grounded commit on the answer slot.
    apply(store, {
      id: ANSWER,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "## Repo overview\n\nThis is **Painted Wolf Code / Lycaon**.",
      visibility: "transcript",
      draft_status: "committed",
      draft_version_count: 2,
      grounding: { traced: true, cited_evidence: [{ handle: "read#1" }] },
      created_at: "2026-07-07T22:26:03Z",
    } as Message);

    items = see();
    prose = proseRow(items);
    expect(prose?.kind).toBe("assistant");
    expect(prose?.text).toContain("Repo overview");

    // Completed chips from earlier steps survive the whole turn with full fidelity.
    const done = displayed(store, { ...activeTurn, status: "done" }).items;
    const finalChip = toolItemByKey(done, listChipKey);
    expect(finalChip?.part.tool).toBe("list_dir");
    expect(finalChip?.part.args).toEqual({ path: "." });
  });

  it("hides rejected task cards unless verbose — never sticky Spawning worker", () => {
    const store: Store = { messages: [] };
    const reject =
      "Rejected: You called **`task`** but no session **`## Progress`** checklist exists yet.\n\nCode: PROGRESS_MISSING";

    apply(store, {
      id: "m-user",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "Tell me about this repo",
      created_at: "t0",
    } as Message);
    // Host promoted task tool_calls mid-turn, then settled the batch with reject results.
    apply(store, {
      id: SLOT,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      visibility: "transcript",
      draft_status: "committed",
      created_at: "t1",
      tool_calls: [
        {
          id: "call_task_1",
          name: "task",
          args: {
            agent_type: "repo-researcher",
            brief: { goal: "Focus on the CLI and core runtime architecture.", done_when: ["Return results."] },
          },
        },
        {
          id: "call_task_2",
          name: "task",
          args: {
            agent_type: "repo-researcher",
            brief: { goal: "Investigate SDK, plugin, and server architecture.", done_when: ["Return results."] },
          },
        },
      ],
    } as Message);
    apply(store, {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: reject,
      created_at: "t2",
      tool_result: {
        content: reject,
        tool: "task",
        tool_call_id: "call_task_1",
        assistant_message_id: SLOT,
        outcome: "rejected",
        code: "PROGRESS_MISSING",
        ui_visibility: "benign",
      },
    } as Message);
    apply(store, {
      id: "tr2",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: reject,
      created_at: "t3",
      tool_result: {
        content: reject,
        tool: "task",
        tool_call_id: "call_task_2",
        assistant_message_id: SLOT,
        outcome: "rejected",
        code: "PROGRESS_MISSING",
        ui_visibility: "benign",
      },
    } as Message);

    // A retry slot leaves settled task cards settled.
    apply(store, {
      id: "slot-retry",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      kind: "draft",
      visibility: "transcript",
      draft_status: "committed",
      created_at: "t4",
      tool_calls: [
        {
          id: "call_progress",
          name: "update_progress",
          args: { content: "## Progress\n- [ ] overview" },
        },
      ],
    } as Message);
    apply(store, {
      id: "tr3",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"status":"updated"}',
      created_at: "t5",
      tool_result: {
        content: '{"status":"updated"}',
        tool: "update_progress",
        tool_call_id: "call_progress",
        assistant_message_id: "slot-retry",
        outcome: "completed",
      },
    } as Message);

    const quiet = createTranscriptDisplayProjector()(store.messages, undefined, {
      verboseMode: false,
    })[0]!;
    expect(toolItemsByName(quiet, "task")).toHaveLength(0);

    const verbose = createTranscriptDisplayProjector()(store.messages, undefined, {
      verboseMode: true,
    })[0]!;
    const taskCards = toolItemsByName(verbose, "task");
    expect(taskCards).toHaveLength(2);
    for (const card of taskCards) {
      expect(card.part.status).toBe("error");
      expect(card.part.status).not.toBe("running");
    }
  });

  it("keeps completed chips visible when live-turn signals clear (home navigation)", () => {
    const store: Store = {
      messages: [
        { id: "m-user", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t0" } as Message,
        {
          id: SLOT,
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          visibility: "internal",
          draft_status: "live",
          created_at: "t1",
          tool_calls: [
            { id: "call_1", name: "list_dir", args: { path: "." } },
            { id: "call_2", name: "read", args: { path: "README.md" } },
          ],
        } as Message,
        toolRow("t1", "call_1", "list_dir", "[list#1]\n{}", "t2"),
        toolRow("t2", "call_2", "read", '[read#1]\n{"content":"hi"}', "t3"),
      ],
    };
    const listChipKey = `${SLOT}:call_1`;
    const readChipKey = `${SLOT}:call_2`;

    const idle = displayed(store, undefined);
    expect(toolItemByKey(idle.items, listChipKey)?.part.tool).toBe("list_dir");
    expect(toolItemByKey(idle.items, readChipKey)?.part.tool).toBe("read");
  });

  it("keeps completed chips after home navigation when draft ts sorts slot after tools", () => {
    // Hydrate order (ORDER BY ts): tool results precede the draft-bumped slot row.
    const store: Store = {
      messages: [
        { id: "m-user", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "2026-07-07T22:25:26Z" } as Message,
        toolRow("t1", "call_1", "list_dir", "[list#1]\n{}", "2026-07-07T22:25:29Z"),
        toolRow("t2", "call_2", "read", '[read#1]\n{"content":"hi"}', "2026-07-07T22:25:34Z"),
        {
          id: SLOT,
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "This is **Painted Wolf Code / Lycaon** — streaming draft.",
          visibility: "internal",
          draft_status: "live",
          status: "streaming",
          created_at: "2026-07-07T22:25:42Z",
          tool_calls: [
            { id: "call_1", name: "list_dir", args: { path: "." } },
            { id: "call_2", name: "read", args: { path: "README.md" } },
          ],
        } as Message,
      ],
    };
    const listChipKey = `${SLOT}:call_1`;
    const readChipKey = `${SLOT}:call_2`;

    const idle = displayed(store, undefined);
    expect(toolItemByKey(idle.items, listChipKey)?.part.tool).toBe("list_dir");
    expect(toolItemByKey(idle.items, readChipKey)?.part.tool).toBe("read");
    expect(idle.items.some((item) => item.kind === "draft" && item.key === SLOT)).toBe(
      true,
    );
  });

  it("keeps completed chips visible through a tool-phase llmActive flap", () => {
    // Completed tool rows remain visible between model iterations.
    const store: Store = {
      messages: [
        { id: "m-user", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t0" } as Message,
        {
          id: SLOT,
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          visibility: "internal",
          draft_status: "live",
          created_at: "t1",
          tool_calls: [{ id: "call_1", name: "list_dir", args: { path: "." } }],
        } as Message,
        toolRow("t1", "call_1", "list_dir", "[list#1]\n{}", "t2"),
      ],
    };
    const listChipKey = `${SLOT}:call_1`;

    // Tool execution can outlive its model call.
    const flap = displayed(store, undefined);
    expect(toolItemByKey(flap.items, listChipKey)?.part.tool).toBe("list_dir");

    const next = displayed(store, activeTurn);
    expect(toolItemByKey(next.items, listChipKey)?.part.tool).toBe("list_dir");
  });

  it("shows the final prose when the commit snapshot drops an unexecuted tool call", () => {
    // The committed answer clears an unexecuted tool batch.
    const store: Store = { messages: [] };

    apply(store, {
      id: "m-user",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "go",
      seq: 1,
      created_at: "t0",
    } as Message);
    // Rejected mid-run step: prose + a task call that never ran.
    apply(store, {
      id: "slot-attempt",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "The web-researcher failed. I will retry it.",
      kind: "draft",
      visibility: "transcript",
      draft_status: "committed",
      seq: 2,
      created_at: "t1",
      tool_calls: [{ id: "call_task", name: "task", args: { agent_type: "web-researcher" } }],
    } as Message);
    // Answer slot streams with a dangling update_progress call...
    apply(store, {
      id: SLOT,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "The web-researcher leg failed due to a provider timeout.",
      visibility: "internal",
      draft_status: "live",
      seq: 3,
      created_at: "t2",
      tool_calls: [{ id: "call_progress", name: "update_progress", args: {} }],
    } as Message);
    // ...and commits as pure prose: the snapshot carries no tool_calls.
    apply(store, {
      id: SLOT,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "The web-researcher leg failed due to a provider timeout.",
      kind: "draft",
      visibility: "transcript",
      draft_status: "committed",
      seq: 4,
      created_at: "t3",
    } as Message);

    const slotRow = store.messages.find((m) => m.id === SLOT);
    expect(slotRow?.tool_calls).toBeUndefined();

    const quiet = createTranscriptDisplayProjector()(store.messages, undefined, {
      verboseMode: false,
    })[0]!;
    const answer = quiet.find(
      (item): item is Extract<TranscriptItem, { kind: "assistant" }> =>
        item.kind === "assistant" && item.key === SLOT,
    );
    expect(answer?.text).toContain("provider timeout");
  });

  it("bridges chip names from tool_result.tool when the assistant batch is gone (hydration)", () => {
    // Hydrated result rows supply tool names when the final slot has no tool_calls.
    const store: Store = {
      messages: [
        { id: "m-user", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t0" } as Message,
        {
          id: SLOT,
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "final",
          visibility: "transcript",
          created_at: "t1",
        } as Message,
        toolRow("t1", "call_1", "list_dir", "[list#1]\n{}", "t2"),
      ],
    };
    const items = createTranscriptDisplayProjector()(store.messages, undefined)[0]!;
    const chip = toolItemByKey(items, `${SLOT}:call_1`);
    expect(chip?.part.tool).toBe("list_dir");
    expect(chip?.part.status).toBe("completed");
  });
});
