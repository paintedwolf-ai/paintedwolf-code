import { describe, expect, it } from "vitest";
import {
  retainTranscriptItemIdentity,
  sameTranscriptItem,
  transcriptItemsByKey,
} from "./transcript-display-projection.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";

const planItem = (overrides?: {
  content?: string;
  status?: string;
  blueprintTitle?: string;
}): TranscriptItem =>
  ({
    kind: "blueprint_card",
    key: "msg-1",
    content: overrides?.content ?? "# Plan\n- step",
    meta: {
      blueprint_path: ".paintedwolf/blueprints/a.md",
      blueprint_title: overrides?.blueprintTitle ?? "A plan",
      revision_key: "r1",
      status: overrides?.status ?? "draft",
      phase: "plan",
    },
    view: {},
  }) as unknown as TranscriptItem;

const checkpointItem = (status = "pending"): TranscriptItem =>
  ({
    kind: "checkpoint",
    key: "checkpoint:cp-1",
    parentMessageId: "msg-2",
    meta: { checkpoint_id: "cp-1", status, kind: "tool_approval" },
  }) as unknown as TranscriptItem;

const progressItem = (
  overrides?: Partial<Extract<TranscriptItem, { kind: "progress_update" }>>,
): TranscriptItem => ({
  kind: "progress_update",
  key: "progress-1",
  initial: true,
  steps: [{ state: "pending", label: "Build the lexer" }],
  changes: [],
  ...overrides,
});

const retain = (previous: readonly TranscriptItem[], next: readonly TranscriptItem[]) =>
  retainTranscriptItemIdentity(transcriptItemsByKey(previous), next);

describe("retainTranscriptItemIdentity", () => {
  it("reuses the prior object when nothing rendered changed", () => {
    const prev = [planItem(), checkpointItem()];
    const next = [planItem(), checkpointItem()];
    const out = retain(prev, next);
    expect(out[0]).toBe(prev[0]);
    expect(out[1]).toBe(prev[1]);
  });

  it("yields the fresh object when the plan content changes", () => {
    const prev = [planItem()];
    const next = [planItem({ content: "# Plan\n- step\n- step two" })];
    expect(retain(prev, next)[0]).toBe(next[0]);
  });

  it("yields the fresh object when a plan field outside content changes", () => {
    const prev = [planItem()];
    const next = [planItem({ blueprintTitle: "Renamed plan" })];
    expect(retain(prev, next)[0]).toBe(next[0]);
  });

  it("yields the fresh object when a checkpoint resolves", () => {
    const prev = [checkpointItem("pending")];
    const next = [checkpointItem("approved")];
    expect(retain(prev, next)[0]).toBe(next[0]);
  });

  it("reuses an unchanged progress snapshot", () => {
    const prev = [progressItem()];
    const next = [progressItem()];
    expect(retain(prev, next)[0]).toBe(prev[0]);
  });

  it("yields fresh progress when a rendered field changes", () => {
    const prev = [progressItem()];
    const next = [
      progressItem({
        summary: { change_count: 1, total_steps: 1, pending: 0, done: 1, na: 0 },
      }),
    ];
    expect(retain(prev, next)[0]).toBe(next[0]);
  });

  it("applies to every row kind, so an unchanged prose row keeps its object", () => {
    const prior: TranscriptItem = { kind: "assistant", key: "msg-3", text: "hi" };
    const fresh: TranscriptItem = { kind: "assistant", key: "msg-3", text: "hi" };
    expect(retain([prior], [fresh])[0]).toBe(prior);
  });

  it("a user row that gained artifact references is a new row", () => {
    const prior: TranscriptItem = { kind: "user", key: "u1", text: "look" };
    const fresh: TranscriptItem = { kind: "user", key: "u1", text: "look", artifactIds: ["art-1"] };
    expect(retain([prior], [fresh])[0]).toBe(fresh);
  });

  it("never pairs rows of different kinds under one key", () => {
    const prior: TranscriptItem = { kind: "draft", key: "m", text: "hi" };
    const fresh: TranscriptItem = { kind: "assistant", key: "m", text: "hi" };
    expect(sameTranscriptItem(prior, fresh)).toBe(false);
    expect(retain([prior], [fresh])[0]).toBe(fresh);
  });

  it("passes through when there is no previous build", () => {
    const next = [planItem()];
    expect(retainTranscriptItemIdentity(new Map(), next)[0]).toBe(next[0]);
  });
});
