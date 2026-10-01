import { describe, expect, it } from "vitest";
import {
  latestMessageSnapshot,
  messagesEqual,
} from "./messages-equal.ts";
import type { Message } from "../../../api/types.ts";

const user = (id: string, content: string): Message => ({
  id,
  role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
  content,
  created_at: "t",
});

const assistant = (
  id: string,
  content: string,
  tool_calls?: Message["tool_calls"],
): Message => ({
  id,
  role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
  content,
  created_at: "t",
  tool_calls,
});

describe("latestMessageSnapshot", () => {
  it("an absent tool_calls in the newer snapshot clears the batch", () => {
    // A commit snapshot without tool calls clears the pending batch.
    const existing: Message = {
      ...assistant("a1", "The web-researcher leg failed.", [
        { id: "call_progress", name: "update_progress", args: {} },
      ]),
      kind: "draft",
      draft_status: "live",
      seq: 443,
    };
    const commit: Message = {
      ...assistant("a1", "The web-researcher leg failed."),
      kind: "draft",
      draft_status: "committed",
      seq: 445,
    };
    const kept = latestMessageSnapshot(existing, commit);
    expect(kept).toBe(commit);
    expect(kept.tool_calls).toBeUndefined();
  });

  it("keeps the existing row when the incoming snapshot is strictly older", () => {
    const existing: Message = { ...assistant("a1", "newer"), seq: 10 };
    const stale: Message = { ...assistant("a1", "older"), seq: 9 };
    expect(latestMessageSnapshot(existing, stale)).toBe(existing);
  });

  it("adopts an equal-seq snapshot (idempotent re-send)", () => {
    const existing: Message = { ...assistant("a1", "row"), seq: 7 };
    const resend: Message = { ...assistant("a1", "row"), seq: 7 };
    expect(latestMessageSnapshot(existing, resend)).toBe(resend);
  });

  it("adopts an unsequenced snapshot when the row has never been seq'd", () => {
    // Live deltas can update a row without a durable sequence.
    const existing: Message = assistant("a1", "row");
    const unsequenced: Message = assistant("a1", "replacement");
    expect(latestMessageSnapshot(existing, unsequenced)).toBe(unsequenced);
  });

  it("adopts an unsequenced delta while the existing row is still streaming", () => {
    // Checkpointed rows continue accepting live deltas until completion.
    const existing: Message = {
      ...assistant("a1", "row"),
      seq: 7,
      status: "streaming",
    };
    const unsequenced: Message = { ...assistant("a1", "replacement"), status: "streaming" };
    expect(latestMessageSnapshot(existing, unsequenced)).toBe(unsequenced);
  });

  it("rejects an unsequenced delta once the row is a seq'd, finalized snapshot", () => {
    // Unsequenced live deltas cannot replace a settled durable row.
    const existing: Message = {
      ...assistant("a1", "the fix landed"),
      seq: 7,
      status: "complete",
    };
    const staleProjection: Message = assistant("a1", "the fix lan");
    expect(latestMessageSnapshot(existing, staleProjection)).toBe(existing);
  });

  it("a genuinely higher-seq durable write wins even over a finalized row", () => {
    const existing: Message = {
      ...assistant("a1", "the fix landed"),
      seq: 7,
      status: "complete",
    };
    const nextDurableWrite: Message = { ...assistant("a1", "edited"), seq: 8 };
    expect(latestMessageSnapshot(existing, nextDurableWrite)).toBe(nextDurableWrite);
  });

  it("replaces every field wholesale — absent means gone", () => {
    const existing: Message = {
      ...assistant("a1", "the fix landed"),
      seq: 3,
      workflow_run_id: "0ea26756-a763-4c2a-b933-fa6b64064a20",
      grounding: {
        traced: true,
        checks: [
          { id: "path_citations", label: "Path citations", status: "passed" as const },
        ],
      },
    };
    const snapshot: Message = { ...assistant("a1", "the fix landed"), seq: 4 };
    const kept = latestMessageSnapshot(existing, snapshot);
    expect(kept.workflow_run_id).toBeUndefined();
    expect(kept.grounding).toBeUndefined();
  });
});

describe("messagesEqual", () => {
  it("detects progress summary changes", () => {
    const progressMessage = (done: number): Message => ({
      id: "progress-summary",
      role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
      content: "",
      kind: "progress_update",
      created_at: "t",
      progress_update: {
        seq: 1,
        summary: {
          change_count: 13,
          total_steps: 20,
          pending: 20 - done,
          done,
          na: 0,
        },
      },
    });
    expect(messagesEqual([progressMessage(0)], [progressMessage(13)])).toBe(false);
  });

  it("detects worker_summary grounding patches", () => {
    const base: Message = {
      id: "ws1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "summary",
      created_at: "t",
      worker_summary: {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "complete",
        envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="complete"></task>',
      },
    };
    const withGrounding: Message = {
      ...base,
      worker_summary: {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "complete",
        envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="complete"></task>',
        grounding: {
          traced: true,
          checks: [{ id: "path_citations", label: "Path citations", status: "passed" as const }],
        },
      },
    };
    expect(messagesEqual([base], [withGrounding])).toBe(false);
  });

  it("detects server append-only tail so worker and coordinator rows apply", () => {
    const client = [user("u1", "Hi"), assistant("a1", "Hello")];
    const server = [
      ...client,
      assistant("ws1", "x".repeat(50_000), [{ id: "tc1", name: "task", args: {} }]),
      assistant("a2", "Your game is ready."),
    ];
    expect(messagesEqual(client, server)).toBe(false);
  });

  it("detects streaming updates on the last message", () => {
    const a = [user("u1", "Hi"), assistant("a1", "Hel")];
    const b = [user("u1", "Hi"), assistant("a1", "Hello")];
    expect(messagesEqual(a, b)).toBe(false);
  });

  it("detects mid-transcript assistant tool_calls when the tail is unchanged", () => {
    const client = [
      user("u1", "Hi"),
      assistant("a1", "I'll read the file."),
      assistant("a2", "Done."),
    ];
    const server = [
      user("u1", "Hi"),
      assistant("a1", "I'll read the file.", [
        { id: "tc1", name: "read", args: { path: "src/x.go" } },
      ]),
      assistant("a2", "Done."),
    ];
    expect(messagesEqual(client, server)).toBe(false);
  });

  it("detects tool_result patches on non-tail rows", () => {
    const base = [
      user("u1", "Hi"),
      assistant("a1", "", [{ id: "tc1", name: "read", args: { path: "x.go" } }]),
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "partial",
        tool_result: { content: "partial" },
        created_at: "t",
      },
      assistant("a2", "Done."),
    ];
    const client = [...base];
    const server = [
      ...base.slice(0, 2),
      {
        ...base[2]!,
        content: "package main",
        tool_result: { content: "package main", outcome: "completed" as const },
      },
      base[3]!,
    ];
    expect(messagesEqual(client, server)).toBe(false);
  });

  it("detects structured tool result patches", () => {
    const base: Message = {
      id: "tr1",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "queued",
      tool_result: {
        content: "queued",
        dispatch: { worker_id: "job-1" },
      },
      created_at: "t",
    };
    const patched: Message = {
      ...base,
      tool_result: {
        ...base.tool_result!,
        dispatch: { worker_id: "job-1", child_session_id: "child-1" },
      },
    };

    expect(messagesEqual([base], [patched])).toBe(false);
  });

  it("detects prefix id mismatch", () => {
    const a = [user("u1", "Hi")];
    const b = [user("u2", "Hi"), assistant("a1", "x")];
    expect(messagesEqual(a, b)).toBe(false);
  });

  it("treats identical snapshots as equal", () => {
    const msgs = [user("u1", "Hi"), assistant("a1", "Hello")];
    expect(messagesEqual(msgs, msgs)).toBe(true);
    expect(messagesEqual(msgs, [...msgs])).toBe(true);
  });

  it("detects a resident tail row the baseline has not caught up to", () => {
    // An SSE append can land before a slower baseline fetch returns.
    const server = [user("u1", "Hi")];
    const client = [...server, user("u2", "follow-up")];
    expect(messagesEqual(client, server)).toBe(false);
  });

  it("detects the stream-done status flip so settled rows leave streaming", () => {
    // A terminal chunk may carry only the status transition.
    const streaming = [{ ...assistant("a1", "Hello"), status: "streaming" as const }];
    const settled = [{ ...assistant("a1", "Hello"), status: "complete" as const }];
    expect(messagesEqual(streaming, settled)).toBe(false);
    expect(messagesEqual(settled, [{ ...assistant("a1", "Hello") }])).toBe(false);
  });

  it("detects a generating_tokens change on an otherwise identical row", () => {
    const before = [{ ...assistant("a1", "Hello"), generating_tokens: 10 }];
    const after = [{ ...assistant("a1", "Hello"), generating_tokens: undefined }];
    expect(messagesEqual(before, after)).toBe(false);
  });

  it("detects navigation_refs and content_parts patches", () => {
    const bare = assistant("a1", "Hello");
    const withRefs: Message = {
      ...bare,
      navigation_refs: [{
        id: "ref-1", syntax: "code", status: "resolved", explicit: true, mention: "src/a.ts",
        project_id: "p1",
        root_id: "root-1",
        path: "src/a.ts",
        entry_kind: "file",
      }],
    };
    expect(messagesEqual([bare], [withRefs])).toBe(false);
    const withParts: Message = {
      ...bare,
      content_parts: [{
        content: "Hello",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
      }],
    };
    expect(messagesEqual([bare], [withParts])).toBe(false);
  });
});
