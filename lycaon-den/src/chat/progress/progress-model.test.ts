import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import {
  isProgressArchivedInTranscript,
  isTerminalProgressStep,
  liveProgressItems,
  progressChangesToSteps,
  progressHistoryFromMessages,
  progressUpdateSummaryLabel,
} from "./progress-model.ts";

function hostMessage(
  message: Pick<Message, "id" | "created_at"> & Partial<Message>,
): Message {
  return {
    role: "system",
    origin: "host",
    authority: "system",
    trust_tier: "trusted",
    content: "",
    ...message,
  };
}

describe("progress-model", () => {
  it("treats done and n/a as terminal", () => {
    expect(isTerminalProgressStep("done")).toBe(true);
    expect(isTerminalProgressStep("na")).toBe(true);
    expect(isTerminalProgressStep("pending")).toBe(false);
    expect(isTerminalProgressStep("active")).toBe(false);
  });

  it("keeps only open rows in the live checklist", () => {
    expect(
      liveProgressItems([
        { state: "done", label: "shipped" },
        { state: "pending", label: "verify" },
        { state: "na", label: "dropped" },
      ]),
    ).toEqual([{ state: "pending", label: "verify" }]);
  });

  it("progressChangesToSteps renders delta rows for transcript strips", () => {
    expect(
      progressChangesToSteps([
        { kind: "done", label: "Ship auth", state: "done" },
        {
          kind: "updated",
          label: "Verify build",
          prev_label: "Run tests",
          state: "pending",
        },
      ]),
    ).toEqual([
      { label: "Ship auth", state: "done" },
      { label: "Run tests → Verify build", state: "pending" },
    ]);
  });

  it("formats and retains summarized progress updates", () => {
    const summary = {
      change_count: 14,
      total_steps: 20,
      pending: 6,
      done: 13,
      na: 1,
    };
    expect(progressUpdateSummaryLabel(summary, false)).toBe(
      "14 progress items updated · 20 total · 6 pending · 13 done · 1 not applicable",
    );

    const messages: Message[] = [hostMessage({
      id: "summary",
      kind: "progress_update",
      created_at: "2026-06-18T12:00:01Z",
      progress_update: { seq: 1, summary },
    })];
    expect(progressHistoryFromMessages(messages)).toEqual([{
      key: "summary:summary",
      kind: "summary",
      label: "14 progress items updated · 20 total · 6 pending · 13 done · 1 not applicable",
      state: "summary",
      ts: "2026-06-18T12:00:01Z",
    }]);
  });

  it("projects ordered host progress events without reconstructing state", () => {
    const messages: Message[] = [
      hostMessage({
        id: "complete",
        kind: "progress_complete",
        ord: 3,
        created_at: "2026-08-31T14:31:29Z",
        progress_complete: {
          seq: 1,
          steps: [
            { state: "done", label: "Scaffold crate" },
            { state: "done", label: "Run registry (kellnr) in Docker" },
          ],
        },
      }),
      hostMessage({
        id: "initial",
        kind: "progress_update",
        ord: 1,
        created_at: "2026-08-31T13:43:13Z",
        progress_update: {
          seq: 1,
          initial: true,
          steps: [
            { state: "pending", label: "Scaffold crate" },
            { state: "pending", label: "Run registry" },
          ],
        },
      }),
      hostMessage({
        id: "rescope",
        kind: "progress_update",
        ord: 2,
        created_at: "2026-08-31T13:43:24Z",
        progress_update: {
          seq: 2,
          changes: [{
            kind: "updated",
            label: "Run registry in Docker",
            prev_label: "Run registry",
            state: "pending",
          }],
        },
      }),
    ];

    expect(progressHistoryFromMessages(messages)).toEqual([
      {
        key: "initial:step:0",
        kind: "created",
        label: "Scaffold crate",
        state: "pending",
        ts: "2026-08-31T13:43:13Z",
      },
      {
        key: "initial:step:1",
        kind: "created",
        label: "Run registry",
        state: "pending",
        ts: "2026-08-31T13:43:13Z",
      },
      {
        key: "rescope:change:0",
        kind: "updated",
        label: "Run registry → Run registry in Docker",
        state: "pending",
        ts: "2026-08-31T13:43:24Z",
      },
      {
        key: "complete:complete:0",
        kind: "completed",
        label: "Scaffold crate",
        state: "done",
        ts: "2026-08-31T14:31:29Z",
      },
      {
        key: "complete:complete:1",
        kind: "completed",
        label: "Run registry (kellnr) in Docker",
        state: "done",
        ts: "2026-08-31T14:31:29Z",
      },
    ]);
  });

  it("keeps completed rows before progress from a follow-up turn", () => {
    const messages: Message[] = [
      hostMessage({
        id: "first-run",
        kind: "progress_complete",
        ord: 1,
        created_at: "t1",
        progress_complete: {
          seq: 1,
          steps: [{ state: "done", label: "Ship crate" }],
        },
      }),
      hostMessage({
        id: "follow-up",
        kind: "progress_update",
        ord: 2,
        created_at: "t2",
        progress_update: {
          seq: 3,
          initial: true,
          steps: [{ state: "pending", label: "Review auth behavior" }],
        },
      }),
    ];

    expect(progressHistoryFromMessages(messages).map((entry) => ({
      kind: entry.kind,
      label: entry.label,
      state: entry.state,
    }))).toEqual([
      { kind: "completed", label: "Ship crate", state: "done" },
      { kind: "created", label: "Review auth behavior", state: "pending" },
    ]);
  });

  it("isProgressArchivedInTranscript matches an all-terminal checklist to chat snapshot", () => {
    const steps = [
      { state: "done", label: "Run tests" },
      { state: "na", label: "Skipped deploy" },
    ] as const;
    const messages: Message[] = [
      hostMessage({
        id: "pc-1",
        kind: "progress_complete",
        progress_complete: { steps: [...steps], seq: 1 },
        created_at: "2026-06-18T12:00:00Z",
      }),
    ];
    expect(isProgressArchivedInTranscript(steps, messages)).toBe(true);
    expect(
      isProgressArchivedInTranscript(
        [{ state: "done", label: "Run tests" }],
        messages,
      ),
    ).toBe(false);
    expect(
      isProgressArchivedInTranscript(
        [{ state: "pending", label: "Fix tests" }],
        messages,
      ),
    ).toBe(false);
  });
});
