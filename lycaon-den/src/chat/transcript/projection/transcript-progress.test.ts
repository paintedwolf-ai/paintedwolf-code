import { describe, expect, it } from "vitest";
import type { ProgressChange, ProgressStep } from "../../../api/types.ts";
import { finalizeTranscriptItems, messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("transcript-items", () => {

  it("turns a progress_complete message into a completed-progress item", () => {
    const steps: ProgressStep[] = [
      { state: "done", label: "Survey the codebase" },
      { state: "done", label: "Implement the change" },
    ];
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t1" },
      {
        id: "p1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_complete",
        progress_complete: { steps, seq: 1 },
        created_at: "t2",
      },
    ]);
    const progress = items.find((item) => item.kind === "progress_complete");
    expect(progress).toMatchObject({
      kind: "progress_complete",
      key: "p1",
      steps,
    });
  });

  it("turns an initial progress_update message into a full-plan item", () => {
    const steps: ProgressStep[] = [
      { state: "pending", label: "Survey the codebase" },
      { state: "pending", label: "Implement the change" },
    ];
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t1" },
      {
        id: "pu1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update",
        progress_update: { steps, initial: true, seq: 1 },
        created_at: "t2",
      },
    ]);
    const update = items.find((item) => item.kind === "progress_update");
    expect(update).toMatchObject({
      kind: "progress_update",
      key: "pu1",
      initial: true,
      steps,
      changes: [],
    });
  });

  it("turns a delta progress_update message into a changes item", () => {
    const changes: ProgressChange[] = [
      { kind: "done", label: "Implement the change", state: "done" },
      { kind: "created", label: "Verify", state: "pending" },
    ];
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t1" },
      {
        id: "pu2",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update",
        progress_update: { changes, initial: false, seq: 2 },
        created_at: "t2",
      },
    ]);
    const update = items.find((item) => item.kind === "progress_update");
    expect(update).toMatchObject({
      kind: "progress_update",
      key: "pu2",
      initial: false,
      changes,
    });
  });

  it("keeps a summarized progress_update as summary metadata", () => {
    const summary = {
      change_count: 48,
      total_steps: 48,
      pending: 48,
      done: 0,
      na: 0,
    };
    const items = messagesToTranscriptItems([{
      id: "pu-summary",
      role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
      content: "",
      kind: "progress_update",
      progress_update: { initial: true, summary, seq: 1 },
      created_at: "t2",
    }]);
    expect(items.find((item) => item.kind === "progress_update")).toMatchObject({
      kind: "progress_update",
      key: "pu-summary",
      initial: true,
      steps: [],
      changes: [],
      summary,
    });
  });

  it("transcript projector orders initial plan after prior transcript rows when wire ts is later", () => {
    const steps: ProgressStep[] = [
      { state: "pending", label: "Survey" },
      { state: "pending", label: "Implement" },
      { state: "pending", label: "Verify" },
    ];
    const messages = [
      { id: "u1", ord: 1, role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build it", created_at: "2026-01-01T00:00:00.000Z" },
      {
        id: "a-dispatch",
        ord: 2,
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        created_at: "2026-01-01T00:00:01.000Z",
        tool_calls: [
          { id: "tc1", name: "task", args: { job_id: "job-1", description: "leg 1" } },
          { id: "tc2", name: "task", args: { job_id: "job-2", description: "leg 2" } },
          { id: "tc3", name: "task", args: { job_id: "job-3", description: "leg 3" } },
        ],
      },
      {
        id: "tr1",
        ord: 3,
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-1"}',
        tool_result: { content: '{"job_id":"job-1"}', dispatch: { worker_id: "job-1" } },
        created_at: "2026-01-01T00:00:01.000Z",
      },
      {
        id: "tr2",
        ord: 4,
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-2"}',
        tool_result: { content: '{"job_id":"job-2"}', tool: "task", tool_call_id: "tc2", assistant_message_id: "a-dispatch", dispatch: { worker_id: "job-2" } },
        created_at: "2026-01-01T00:00:01.000Z",
      },
      {
        id: "tr3",
        ord: 5,
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-3"}',
        tool_result: { content: '{"job_id":"job-3"}', tool: "task", tool_call_id: "tc3", assistant_message_id: "a-dispatch", dispatch: { worker_id: "job-3" } },
        created_at: "2026-01-01T00:00:01.000Z",
      },
      {
        id: "pu1",
        ord: 6,
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update" as const,
        progress_update: { steps, initial: true, seq: 1 },
        created_at: "2026-01-01T00:00:02.000Z",
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, undefined)[0]!;
    const kinds = items.map((item) => item.kind);
    const blueprintPathx = kinds.indexOf("progress_update");
    const firstTaskIdx = kinds.indexOf("worker_group");
    expect(blueprintPathx).toBeGreaterThanOrEqual(0);
    expect(firstTaskIdx).toBeGreaterThanOrEqual(0);
    expect(blueprintPathx).toBeGreaterThan(firstTaskIdx);
  });

  it("finalizeTranscriptItems keeps mid-run progress deltas", () => {
    const items = finalizeTranscriptItems([
      {
        kind: "progress_update",
        key: "pu1",
        initial: true,
        steps: [{ state: "pending", label: "Ship auth" }],
        changes: [],
      },
      {
        kind: "progress_update",
        key: "pu2",
        initial: false,
        steps: [],
        changes: [{ kind: "done", label: "Ship auth", state: "done" }],
      },
      { kind: "user", key: "u1", text: "next" },
    ]);
    expect(items.map((item) => item.key)).toEqual(["pu1", "pu2", "u1"]);
  });

  it("transcript projector orders progress deltas after intervening transcript rows", () => {
    const steps: ProgressStep[] = [{ state: "pending", label: "Survey" }];
    const messages = [
      { id: "u1", ord: 1, role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build it", created_at: "2026-01-01T00:00:00.000Z" },
      {
        id: "a1",
        ord: 2,
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        created_at: "2026-01-01T00:00:02.000Z",
        tool_calls: [{ id: "tc1", name: "grep", args: { pattern: "main" } }],
      },
      {
        id: "t1",
        ord: 3,
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "matches",
        tool_result: { content: "matches", tool: "grep", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: "2026-01-01T00:00:02.000Z",
      },
      {
        id: "pu1",
        ord: 4,
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update" as const,
        progress_update: { steps, initial: true, seq: 1 },
        created_at: "2026-01-01T00:00:03.000Z",
      },
      {
        id: "pu2",
        ord: 5,
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update" as const,
        progress_update: {
          initial: false,
          seq: 2,
          changes: [{ kind: "done" as const, label: "Survey", state: "done" as const }],
        },
        created_at: "2026-01-01T00:00:05.000Z",
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, undefined)[0]!;
    const keys = items.map((item) => item.key);
    expect(keys.indexOf("a1:tc1")).toBeLessThan(keys.indexOf("pu1"));
    expect(keys.indexOf("pu2")).toBeGreaterThan(keys.indexOf("pu1"));
  });
});
