import { fileEditPreviewFixture, sourceTextHash } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import type { Message, WorkerTask, WorkflowRun } from "../../api/types.ts";
import {
  buildChatTranscriptBlocks,
  isCatalogSpan,
} from "./workflow-spans.ts";
import { workflowBoundaryLabel } from "./workflow-boundary-label.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "../checkpoint/approval-test-fixtures.ts";

const ambientRun: WorkflowRun = {
  id: "run-ambient",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "boot",
  start_message_id: "m0",
  created_at: "t",
  updated_at: "t",
};

const catalogRun: WorkflowRun = {
  id: "run-a",
  session_id: "s1",
  workflow_id: "plan",
  workflow_version: "1.0.0",
	revision: 1,
  status: "running",
  current_phase: "research",
  start_message_id: "m-start",
  created_at: "t",
  updated_at: "t",
};

describe("workflow-spans", () => {
  it("buckets stamped rows by their persisted workflow_run_id", () => {
    const messages: Message[] = [
      {
        id: "m1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "implement chat",
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
      {
        id: "m2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "reply",
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [ambientRun], ambientRun);
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.kind).toBe("run");
    if (blocks[0]?.kind === "run") {
      expect(blocks[0].runId).toBe("run-ambient");
      expect(blocks[0].ambientSpan).toBe(true);
      expect(isCatalogSpan(blocks[0])).toBe(false);
      expect(blocks[0].items).toHaveLength(2);
    }
  });

  it("splits spans when workflow_run_id changes", () => {
    const messages: Message[] = [
      {
        id: "m1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "ambient",
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
      {
        id: "b1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: "run-a",
        created_at: "t",
      },
      {
        id: "m2",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "in plan",
        workflow_run_id: "run-a",
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [ambientRun, catalogRun],
      ambientRun,
    );
    expect(blocks).toHaveLength(2);
    expect(blocks[0]?.runId).toBe("run-ambient");
    expect(blocks[0]?.ambientSpan).toBe(true);
    expect(blocks[1]?.runId).toBe("run-a");
    expect(blocks[1]?.ambientSpan).toBe(false);
    if (blocks[1]?.kind === "run") {
      expect(blocks[1].startMessageId).toBe("m-start");
      expect(blocks[1].items.map((it) => it.kind)).toEqual([
        "workflow_boundary",
        "user",
      ]);
      expect(blocks[1].items[0]).toMatchObject({
        kind: "workflow_boundary",
        key: "b1",
        label: "Plan started",
      });
    }
  });

  it("span prebuild does not pull tools from other runs into a span block", () => {
    const runA: WorkflowRun = { ...ambientRun, id: "run-a" };
    const runB: WorkflowRun = {
      ...catalogRun,
      id: "run-b",
      workflow_id: "plan",
    };
    const messages = [
      {
        id: "u1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "implement",
        workflow_run_id: "run-a",
        created_at: "t1",
      },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "read done",
        workflow_run_id: "run-a",
        tool_calls: [{ name: "read", id: "call_a", args: { path: "a.go" } }],
        created_at: "t2",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        workflow_run_id: "run-a",
        tool_result: { outcome: "completed" as const, content: "ok" },
        created_at: "t3",
      },
      {
        id: "b-start",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_boundary" as const,
        content: "",
        workflow_run_id: "run-b",
        workflow_boundary: { event: "started" as const, workflow_id: "plan" },
        created_at: "t4",
      },
      {
        id: "u2",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "plan",
        workflow_run_id: "run-b",
        created_at: "t5",
      },
      {
        id: "a2",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "planning",
        visibility: "internal" as const,
        workflow_run_id: "run-b",
        tool_calls: [{ name: "task", id: "call_b", args: { brief: { goal: "plan", done_when: ["Return results."] } } }],
        created_at: "t6",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [runA, runB],
      runB,
      {
        verboseMode: true,
        workers: [{ id: "w1", parent_session_id: "s1", status: "running" } as never],
      },
    );
    expect(blocks).toHaveLength(2);
    const ambientTools = blocks[0]!.items.filter((item) => item.kind === "tool");
    const catalogTools = blocks[1]!.items.filter((item) => item.kind === "tool");
    expect(ambientTools).toHaveLength(1);
    expect(catalogTools).toHaveLength(1);
    if (ambientTools[0]?.kind === "tool") {
      expect(ambientTools[0].part.toolCallId).toBe("call_a");
    }
    if (catalogTools[0]?.kind === "tool") {
      expect(catalogTools[0].part.toolCallId).toBe("call_b");
    }
  });

  it("buildChatTranscriptBlocks uses ambient span for session_create leaf", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: ambientRun.id, created_at: "t" },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [ambientRun], ambientRun);
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.ambientSpan).toBe(true);
    expect(blocks[0]?.runId).toBe(ambientRun.id);
  });

  it("buildChatTranscriptBlocks uses catalog span chrome when plan is leaf", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: catalogRun.id, created_at: "t" },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [catalogRun], catalogRun);
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.ambientSpan).toBe(false);
    expect(blocks[0]?.runId).toBe(catalogRun.id);
  });

  it("keeps a terminal catalog run a catalog span", () => {
    const cancelled: WorkflowRun = { ...catalogRun, status: "canceled" };
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: cancelled.id, created_at: "t" },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [cancelled], cancelled);
    expect(blocks[0]?.ambientSpan).toBe(false);
    expect(isCatalogSpan(blocks[0]!)).toBe(true);
  });

  it("returns empty blocks when no leaf run and no messages", () => {
    expect(buildChatTranscriptBlocks([], [], undefined)).toEqual([]);
  });

  it("buildChatTranscriptBlocks shows history when ambient run is idle (completed)", () => {
    const idleAmbient: WorkflowRun = { ...ambientRun, status: "complete" };
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "prior chat", workflow_run_id: idleAmbient.id, created_at: "t" },
      { id: "m2", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "reply", workflow_run_id: idleAmbient.id, created_at: "t" },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [idleAmbient],
      undefined,
    );
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.items).toHaveLength(2);
    expect(blocks[0]?.runId).toBe(idleAmbient.id);
  });

  it("renders rows whose run has not landed", () => {
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hello", workflow_run_id: ambientRun.id, created_at: "t" },
    ];

    const blocks = buildChatTranscriptBlocks(messages, [], undefined);

    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.runId).toBe(ambientRun.id);
    expect(blocks[0]?.items).toHaveLength(1);
  });

  it("renders a row the host sent without a workflow_run_id", () => {
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hello", created_at: "t" },
    ];

    const blocks = buildChatTranscriptBlocks(messages, [], undefined);

    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.runId).toBe("");
    expect(blocks[0]?.items).toHaveLength(1);
    expect(isCatalogSpan(blocks[0]!)).toBe(false);
  });

  it("does not change a span's shape when its run object lands", () => {
    // Run hydration preserves existing span geometry.
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hello", workflow_run_id: catalogRun.id, created_at: "t" },
    ];

    const before = buildChatTranscriptBlocks(messages, [], undefined);
    const after = buildChatTranscriptBlocks(messages, [catalogRun], catalogRun);

    expect(isCatalogSpan(before[0]!)).toBe(true);
    expect(isCatalogSpan(after[0]!)).toBe(true);
  });

  it("emits at most one span block per workflow run id", () => {
    const cancelledAmbient: WorkflowRun = {
      ...ambientRun,
      status: "canceled",
    };
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build chat", workflow_run_id: cancelledAmbient.id, created_at: "t" },
      {
        id: "slash",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "/plan",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "tc1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: catalogRun.id,
        tool_calls: [{ id: "c1", name: "shallow_glob", args: {} }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "[]",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [cancelledAmbient, catalogRun],
      catalogRun,
    );
    const runIds = blocks.map((b) => b.runId);
    expect(new Set(runIds).size).toBe(runIds.length);
  });

  it("span membership is identical whether workflowRuns is empty or hydrated (no row changes span as runs arrive)", () => {
    // Stamped workflow_run_id preserves membership across run hydration.
    const messages: Message[] = [
      { id: "m-amb", ord: 1, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build chat", workflow_run_id: ambientRun.id, created_at: "t" },
      { id: "a-amb", ord: 2, role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "ok", workflow_run_id: ambientRun.id, created_at: "t" },
      {
        id: "b-start",
        ord: 3,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      { id: "m-cat", ord: 4, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "plan it", workflow_run_id: catalogRun.id, created_at: "t" },
      {
        id: "prog-update",
        ord: 5,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "progress_update",
        content: "step done",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "prog-complete",
        ord: 6,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "progress_complete",
        content: "all done",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
    ];

    // Case A: catalog run NOT yet hydrated (workflowRuns empty; leaf via activeRun).
    const emptyBlocks = buildChatTranscriptBlocks(
      messages,
      [],
      ambientRun,
    );
    // Case B: fully hydrated.
    const hydratedBlocks = buildChatTranscriptBlocks(
      messages,
      [ambientRun, catalogRun],
      ambientRun,
    );

    const membership = (blocks: ReturnType<typeof buildChatTranscriptBlocks>) =>
      blocks.map((b) => ({
        runId: b.runId,
        items: b.items.map((it) => it.kind),
      }));

    expect(membership(emptyBlocks)).toEqual(membership(hydratedBlocks));

    // Both emit one ambient block then one catalog block, in creation (ord) order.
    expect(hydratedBlocks.map((b) => b.runId)).toEqual([
      ambientRun.id,
      catalogRun.id,
    ]);

    // Stamped run IDs assign progress rows before run hydration.
    const emptyCatalog = emptyBlocks.find((b) => b.runId === catalogRun.id);
    expect(emptyCatalog?.items.map((it) => it.kind)).toEqual(
      expect.arrayContaining(["progress_update", "progress_complete"]),
    );
  });

  it("keeps one catalog span for stamped plan-era messages", () => {
    const cancelledAmbient: WorkflowRun = {
      ...ambientRun,
      status: "canceled",
    };
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build chat", workflow_run_id: cancelledAmbient.id, created_at: "t" },
      {
        id: "b-exit",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "internal",
        workflow_boundary: { event: "exited", workflow_id: "implement" },
        workflow_run_id: cancelledAmbient.id,
        created_at: "t",
      },
      {
        id: "slash",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "/plan make tictactoe",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "b-start",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "tc1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: catalogRun.id,
        tool_calls: [{ id: "c1", name: "shallow_glob", args: { pattern: "**/*" } }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "[]",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "tc2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: catalogRun.id,
        tool_calls: [{ id: "c2", name: "write", args: { path: "plan.md" } }],
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [cancelledAmbient, catalogRun],
      catalogRun,
    );
    const catalogBlocks = blocks.filter((b) => b.runId === catalogRun.id);
    expect(catalogBlocks).toHaveLength(1);
    expect(catalogBlocks[0]?.items.length).toBeGreaterThan(0);
  });

  it("keeps ambient exited boundary off catalog span when catalog is active leaf", () => {
    const cancelledAmbient: WorkflowRun = {
      ...ambientRun,
      status: "canceled",
    };
    const messages: Message[] = [
      { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build chat", workflow_run_id: cancelledAmbient.id, created_at: "t" },
      {
        id: "b-exit",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "internal",
        workflow_boundary: { event: "exited", workflow_id: "implement" },
        workflow_run_id: cancelledAmbient.id,
        created_at: "t",
      },
      {
        id: "b-start",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "m2",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "plan work",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [cancelledAmbient, catalogRun],
      catalogRun,
    );
    expect(blocks).toHaveLength(2);
    const ambientBlock = blocks.find((b) => b.runId === cancelledAmbient.id);
    const catalogBlock = blocks.find((b) => b.runId === catalogRun.id);
    expect(ambientBlock?.ambientSpan).toBe(true);
    expect(ambientBlock?.items).toHaveLength(1);
    expect(
      ambientBlock?.items.some((it) => it.kind === "workflow_boundary"),
    ).toBe(false);
    expect(catalogBlock?.ambientSpan).toBe(false);
    expect(catalogBlock?.items.map((it) => it.kind)).toEqual([
      "workflow_boundary",
      "user",
    ]);
    expect(catalogBlock?.items[0]).toMatchObject({
      kind: "workflow_boundary",
      key: "b-start",
      label: "Plan started",
    });
  });

  it("orders terminal workflow boundaries by ord, not span header", () => {
    const messages: Message[] = [
      {
        id: "b-start",
        ord: 1,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "u1",
        ord: 2,
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "plan work",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "a1",
        ord: 3,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "researching",
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
      {
        id: "b-cancel",
        ord: 4,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "canceled", workflow_id: "plan" },
        workflow_run_id: catalogRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [catalogRun], catalogRun);
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.items.map((it) => ({ kind: it.kind, key: it.key }))).toEqual([
      { kind: "workflow_boundary", key: "b-start" },
      { kind: "user", key: "u1" },
      { kind: "assistant", key: "a1" },
      { kind: "workflow_boundary", key: "b-cancel" },
    ]);
    expect(blocks[0]?.items[3]).toMatchObject({
      kind: "workflow_boundary",
      label: "Plan canceled",
    });
  });

  it("formats workflow boundary labels as human name + event", () => {
    expect(
      workflowBoundaryLabel(
        { event: "started", workflow_id: "plan", workflow_version: "1.0.0" },
        catalogRun,
      ),
    ).toBe("Plan started");
    expect(
      workflowBoundaryLabel({ event: "exited", workflow_id: "plan" }, catalogRun),
    ).toBe("Plan finished");
    expect(workflowBoundaryLabel({ event: "exited" }, ambientRun)).toBe(
      "Build finished",
    );
    expect(
      workflowBoundaryLabel({ event: "paused", workflow_id: "plan" }, catalogRun),
    ).toBe("Plan paused");
    expect(
      workflowBoundaryLabel({ event: "failed", workflow_id: "plan" }, catalogRun),
    ).toBe("Plan failed");
    expect(
      workflowBoundaryLabel(
        { event: "paused_on_child", workflow_id: "plan" },
        catalogRun,
      ),
    ).toBe("Plan waiting on nested work");
  });

  it("places merged diffs in the promotion message’s workflow span and ordinal", () => {
    const messages: Message[] = [
      {
        id: "a-task",
        ord: 1,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Dispatching work",
        workflow_run_id: "run-ambient",
        tool_calls: [
          {
            id: "tc-task",
            name: "task",
            args: { job_id: "job-1", description: "Fix readme" },
          },
        ],
        created_at: "2026-01-01T00:00:00.000Z",
      },
      {
        id: "t-task",
        ord: 2,
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-1"}',
        workflow_run_id: "run-ambient",
        tool_result: {
          content: '{"job_id":"job-1"}',
          dispatch: { worker_id: "job-1" },
        },
        created_at: "2026-01-01T00:00:01.000Z",
      },
      {
        id: "b-start",
        ord: 3,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: "run-a",
        workflow_boundary: {
          event: "started",
          workflow_id: "plan",
          workflow_version: "1.0.0",
        },
        created_at: "2026-01-01T00:00:20.000Z",
      },
      {
        id: "a-plan",
        ord: 4,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Planning next",
        workflow_run_id: "run-a",
        created_at: "2026-01-01T00:00:25.000Z",
      },
    ];
    messages.push({
      id: "promotion", ord: 5, role: "tool", origin: "host", authority: "none",
      trust_tier: "trusted", content: "", workflow_run_id: "run-a", created_at: "t5",
      tool_result: { content: "", promotion_previews: [
        fileEditPreviewFixture({ path: "README.md", before: "primary", after: "merged" }),
      ] },
    });
    const blocks = buildChatTranscriptBlocks(
      messages,
      [ambientRun, catalogRun],
      catalogRun,
    );
    expect(blocks).toHaveLength(2);
    const ambientKinds = blocks[0]?.items.map((item) => item.kind) ?? [];
    const catalogKinds = blocks[1]?.items.map((item) => item.kind) ?? [];
    expect(ambientKinds).not.toContain("worker_file_edit");
    expect(catalogKinds).toContain("worker_file_edit");
    expect(catalogKinds.indexOf("worker_file_edit")).toBeGreaterThan(catalogKinds.indexOf("assistant"));
    const edit = blocks[1]?.items.find((item) => item.kind === "worker_file_edit");
    expect(edit).toMatchObject({ anchorMessageId: "promotion", folds: [{ net: { after_sha256: sourceTextHash("merged") } }] });
  });

  it("keeps a checkpoint lifecycle on one key and immutable parent ord", () => {
    const assistant: Message = {
      id: "a-checkpoint",
      ord: 2,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      workflow_run_id: ambientRun.id,
      tool_calls: [
        { id: "call-checkpoint", name: "command", args: { command: "git push" } },
      ],
      created_at: "t",
    };
    const pendingBlocks = buildChatTranscriptBlocks(
      [
        {
          id: "u-checkpoint",
          ord: 1,
          role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
          content: "ship it",
          workflow_run_id: ambientRun.id,
          created_at: "t",
        },
        assistant,
      ],
      [ambientRun],
      ambientRun,
      {
        pendingCheckpoints: [
          {
            checkpointId: "checkpoint-1",
            sessionId: "s1",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "t",
            tool_approval: {
              ...toolApprovalFixture({ command: "git push" }),
              tool_call_id: "call-checkpoint",
            },
          },
        ],
      },
    );
    const pending = pendingBlocks[0]?.items.find(
      (item) => item.kind === "checkpoint",
    );
    expect(pending).toMatchObject({
      key: "checkpoint:checkpoint-1",
      parentMessageId: "a-checkpoint",
      meta: { status: "pending" },
    });

    const resolvedBlocks = buildChatTranscriptBlocks(
      [
        {
          id: "u-checkpoint",
          ord: 1,
          role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
          content: "ship it",
          workflow_run_id: ambientRun.id,
          created_at: "t",
        },
        assistant,
        {
          id: "result-checkpoint",
          ord: 3,
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "denied",
          workflow_run_id: ambientRun.id,
          tool_result: {
            content: "denied",
            tool: "command",
            tool_call_id: "call-checkpoint",
            assistant_message_id: "a-checkpoint",
            ui_visibility: "benign",
            checkpoint_decision: {
              checkpoint_id: "checkpoint-1",
              kind: "tool_approval",
              status: "rejected",
            },
          },
          created_at: "t",
        },
      ],
      [ambientRun],
      ambientRun,
    );
    const resolved = resolvedBlocks[0]?.items.find(
      (item) => item.kind === "checkpoint",
    );
    expect(resolved).toMatchObject({
      key: "checkpoint:checkpoint-1",
      parentMessageId: "a-checkpoint",
      meta: { status: "rejected" },
    });
    expect(assistant.ord).toBe(2);
  });

  it("projects a pending worker checkpoint but keeps its settled decision out of parent chat", () => {
    const messages: Message[] = [
      {
        id: "u-worker-checkpoint",
        ord: 1,
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "delegate",
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
      {
        id: "a-worker-checkpoint",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: ambientRun.id,
        tool_calls: [
          {
            id: "call-task",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "work", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "task-result",
        ord: 3,
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-1","status":"enqueued"}',
        workflow_run_id: ambientRun.id,
        tool_result: {
          content: '{"job_id":"job-1","status":"enqueued"}',
          tool: "task",
          tool_call_id: "call-task",
          assistant_message_id: "a-worker-checkpoint",
          dispatch: {
            worker_id: "job-1",
            child_session_id: "child-1",
          },
          tool_args: { agent_type: "implementer", brief: { goal: "work", done_when: ["Return results."] } },
        },
        created_at: "t",
      },
    ];
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        parent_session_id: "s1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [ambientRun],
      ambientRun,
      {
        workers,
        pendingCheckpoints: [
          {
            checkpointId: "checkpoint-child",
            sessionId: "child-1",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "t",
          },
        ],
      },
    );

    expect(
      blocks[0]?.items.find((item) => item.kind === "checkpoint"),
    ).toMatchObject({
      key: "checkpoint:checkpoint-child",
      parentMessageId: "a-worker-checkpoint",
    });

    for (const verboseMode of [false, true]) {
      const resolvedBlocks = buildChatTranscriptBlocks(
        messages,
        [ambientRun],
        ambientRun,
        {
          workers,
          verboseMode,
          pendingCheckpoints: [],
        },
      );
      expect(
        resolvedBlocks.flatMap((block) => block.items)
          .filter((item) => item.kind === "checkpoint"),
      ).toEqual([]);
    }
  });

  it("injects pending checkpoints in standard chat sessions without workflow_run_id", () => {
    const messages: Message[] = [
      {
        id: "u-plain",
        ord: 1,
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "run a build",
        created_at: "t",
      },
      {
        id: "a-plain",
        ord: 2,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "running command",
        tool_calls: [{ name: "command", id: "call-plain", args: { command: "cargo build" } }],
        created_at: "t",
      },
    ];

    const blocks = buildChatTranscriptBlocks(
      messages,
      [],
      undefined,
      {
        pendingCheckpoints: [
          {
            checkpointId: "chk-plain",
            sessionId: "s-plain",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "t",
            tool_approval: {
              ...toolApprovalFixture({ command: "cargo build" }),
              tool_call_id: "call-plain",
            },
          },
        ],
      },
    );

    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.runId).toBe("");
    const checkpointItem = blocks[0]?.items.find((item) => item.kind === "checkpoint");
    expect(checkpointItem).toBeDefined();
    expect(checkpointItem).toMatchObject({
      key: "checkpoint:chk-plain",
      parentMessageId: "a-plain",
      meta: {
        checkpoint_id: "chk-plain",
        kind: "tool_approval",
        status: "pending",
        tool: "command",
        subject: "cargo build",
      },
    });

    // Ordinal sequence places the pending checkpoint after its causing tool call.
    const kinds = blocks[0]?.items.map((item) => item.kind);
    expect(kinds).toEqual(["user", "assistant", "tool", "checkpoint"]);
  });

  it("extracts rich metadata including location, causing command, and grant title onto pending checkpoints", () => {
    const messages: Message[] = [
      {
        id: "a-rich",
        ord: 2,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "deploying",
        tool_calls: [{ name: "command", id: "call-rich", args: { command: "git push" } }],
        created_at: "t",
      },
    ];

    const fixture = toolApprovalFixture({ command: "git push" });
    fixture.plan.presentation.location = {
      origin: "local repo",
      destination: "github.com",
      origin_kind: "file",
      destination_kind: "service",
    };
    fixture.plan.subject.targets = [{ kind: "action", label: "git push" }];
    fixture.plan.options = [
      approvalOptionFixture({
        id: "opt-1",
        title: "Allow for this chat",
        scope: "chat",
        reask_when: "never",
      }),
    ];
    fixture.plan.recommended_option_id = "opt-1";

    const blocks = buildChatTranscriptBlocks(
      messages,
      [],
      undefined,
      {
        pendingCheckpoints: [
          {
            checkpointId: "chk-rich",
            sessionId: "s-rich",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "t",
            tool_approval: {
              ...fixture,
              tool_call_id: "call-rich",
            },
          },
        ],
      },
    );

    const checkpointItem = blocks[0]?.items.find((item) => item.kind === "checkpoint");
    expect(checkpointItem?.meta).toMatchObject({
      checkpoint_id: "chk-rich",
      status: "pending",
      tool: "command",
      subject: "git push",
      location: "local repo → github.com",
      grant_title: "Allow for this chat",
      grant_scope: "chat",
    });
  });
});
