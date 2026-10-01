import { describe, expect, it } from "vitest";
import type { Message, TurnClock } from "../../../api/types.ts";
import { createCalendarTimeFormat } from "../../../time/calendar-time.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import { withTranscriptTimeRows, type TranscriptTimeRowContext } from "./transcript-time-rows.ts";

const calendar = createCalendarTimeFormat({ locale: "en-US", timeZone: "America/Los_Angeles" });

function message(id: string, ord: number, ts: string, role: Message["role"]): Message {
  return {
    id, ord, created_at: ts, role, content: id, origin: role === "user" ? "user" : "model",
    authority: role === "user" ? "user" : "model", trust_tier: "trusted",
  } as Message;
}

function rows(messages: readonly Message[]): DisplayTranscriptItem[] {
  return messages.map((msg): DisplayTranscriptItem =>
    msg.role === "user"
      ? { kind: "user", key: msg.id, text: msg.content }
      : { kind: "assistant", key: msg.id, text: msg.content },
  );
}

function context(
  messages: readonly Message[],
  patch: Partial<TranscriptTimeRowContext> = {},
): TranscriptTimeRowContext {
  return {
    messageById: new Map(messages.map((msg) => [msg.id, msg])),
    turnClocks: {},
    dayKey: calendar.dayKey,
    ...patch,
  };
}

function clock(opening: string, settledAt: string, patch: Partial<TurnClock> = {}): TurnClock {
  return {
    session_id: "root", opening_message_id: opening, active_ms: 348_000, work_ms: 300_000,
    running: false, settled_at: settledAt, ...patch,
  };
}

const keys = (items: readonly DisplayTranscriptItem[]) => items.map((item) => item.key);

// Local times in Los Angeles: Thursday 4:10 PM, Friday 9:12 AM, Friday 1:40 PM.
const thursdayPrompt = message("u1", 1, "2026-09-10T23:10:00Z", "user");
const thursdayReply = message("a1", 2, "2026-09-10T23:10:05Z", "assistant");
const fridayPrompt = message("u2", 3, "2026-09-11T16:12:00Z", "user");
const fridayReply = message("a2", 4, "2026-09-11T16:12:04Z", "assistant");
const afternoonPrompt = message("u3", 5, "2026-09-11T20:40:00Z", "user");
const afternoonReply = message("a3", 6, "2026-09-11T20:40:03Z", "assistant");
const transcript = [thursdayPrompt, thursdayReply, fridayPrompt, fridayReply, afternoonPrompt, afternoonReply];

describe("withTranscriptTimeRows", () => {
  it("labels each day's first row and times a prompt after an hour of quiet", () => {
    const out = withTranscriptTimeRows(rows(transcript), context(transcript));
    expect(keys(out)).toEqual([
      "time:day:u1", "u1", "a1", "turn-tail:u1",
      "time:day:u2", "u2", "a2", "turn-tail:u2",
      "time:gap:u3", "u3", "a3", "turn-tail:u3",
    ]);
  });

  it("never labels a day twice when a later row carries an earlier timestamp", () => {
    const skewed = [
      thursdayPrompt,
      message("a1", 2, "2026-09-11T16:00:00Z", "assistant"),
      message("t1", 3, "2026-09-10T23:59:00Z", "assistant"),
    ];
    const out = withTranscriptTimeRows(rows(skewed), context(skewed));
    expect(out.filter((item) => item.kind === "time_marker").map((item) => item.key)).toEqual([
      "time:day:u1",
      "time:day:a1",
    ]);
  });

  it("follows each settled turn with its tail, before the next turn opens", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, {
        turnClocks: {
          u1: clock("u1", "2026-09-10T23:16:00Z"),
          u2: clock("u2", "2026-09-11T16:20:00Z"),
          u3: clock("u3", "2026-09-11T20:41:00Z", { running: true, running_at: "2026-09-11T20:40:00Z" }),
        },
      }),
    );
    expect(keys(out)).toEqual([
      "time:day:u1", "u1", "a1", "turn-tail:u1",
      "time:day:u2", "u2", "a2", "turn-tail:u2",
      "time:gap:u3", "u3", "a3", "turn-tail:u3",
    ]);
    const tail = out.find((item) => item.key === "turn-tail:u1");
    expect(tail).toMatchObject({
      kind: "turn_tail", anchorMessageId: "a1", workMs: 300_000, activeMs: 348_000,
      settledAt: Date.parse("2026-09-10T23:16:00Z"),
    });
  });

  it("keeps the newest turn open while the session still works on it", () => {
    // A parked wait settles the clock until the wake reopens it.
    const turnClocks = {
      u2: clock("u2", "2026-09-11T16:20:00Z"),
      u3: clock("u3", "2026-09-11T20:41:00Z"),
    };
    const live = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { turnClocks, sessionLive: true }),
    );
    expect(keys(live).slice(-5)).toEqual(["turn-tail:u2", "time:gap:u3", "u3", "a3", "turn-tail:u3"]);
    expect(live.at(-1)).toMatchObject({ settledAt: null });

    const idle = withTranscriptTimeRows(rows(transcript), context(transcript, { turnClocks }));
    expect(keys(idle).at(-1)).toBe("turn-tail:u3");
    expect(keys(live)).toEqual(keys(idle));
    expect(idle.at(-1)).toMatchObject({ settledAt: Date.parse("2026-09-11T20:41:00Z") });
  });

  it("measures the quiet gap from when the previous turn finished", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, {
        turnClocks: {
          u2: clock("u2", "2026-09-11T20:00:00Z"),
          u3: clock("u3", "", { running: true, running_at: "2026-09-11T20:40:00Z", settled_at: undefined }),
        },
      }),
    );
    expect(keys(out)).not.toContain("time:gap:u3");
  });

  it("keeps a host continuation inside its opening turn", () => {
    // A prompt sent while a turn runs joins that turn instead of opening one.
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript.map(msg => msg.id === "u3" ? { ...msg, kind: "user_continuation" } : msg), {
        turnClocks: { u2: clock("u2", "2026-09-11T20:41:00Z") },
      }),
    );
    expect(keys(out).slice(-3)).toEqual(["u3", "a3", "turn-tail:u2"]);
  });

  it("marks the first row that arrived after the seen stamp, including an unread prompt", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { unreadSince: Date.parse("2026-09-11T20:39:00Z") }),
    );
    expect(keys(out).slice(-5)).toEqual(["time:gap:u3", "unread", "u3", "a3", "turn-tail:u3"]);
    expect(out.find((item) => item.kind === "unread_marker")).toMatchObject({ anchorMessageId: "u3" });
  });

  it("marks the reply when the prompt was sent before the seen stamp", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { unreadSince: Date.parse("2026-09-11T20:40:01Z") }),
    );
    expect(keys(out).slice(-5)).toEqual(["time:gap:u3", "u3", "unread", "a3", "turn-tail:u3"]);
    expect(out.find((item) => item.kind === "unread_marker")).toMatchObject({ anchorMessageId: "a3" });
  });

  it("places no unread marker and no leading day marker when hasMoreBefore is true and unread boundary is not resident", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { unreadSince: Date.parse("2026-09-10T00:00:00Z"), hasMoreBefore: true }),
    );
    expect(out.some((item) => item.kind === "unread_marker")).toBe(false);
    expect(out.some((item) => item.key === "time:day:u1")).toBe(false);
    expect(out.some((item) => item.key === "time:day:u2")).toBe(true);
  });

  it("places unread marker when hasMoreBefore is true but boundary is resident", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { unreadSince: Date.parse("2026-09-11T16:12:02Z"), hasMoreBefore: true }),
    );
    expect(out.some((item) => item.kind === "unread_marker")).toBe(true);
    expect(out.find((item) => item.kind === "unread_marker")).toMatchObject({ anchorMessageId: "a2" });
  });

  it("places no new marker when everything was seen", () => {
    const out = withTranscriptTimeRows(
      rows(transcript),
      context(transcript, { unreadSince: Date.parse("2026-09-12T00:00:00Z") }),
    );
    expect(out.some((item) => item.kind === "unread_marker")).toBe(false);
  });
});


describe("pending transcript time rows", () => {
  const pending = (id: string, ts: string) => ({
    kind: "prompt" as const, operationId: id, text: id,
    state: "sending" as const, createdAt: Date.parse(ts),
  });

  it.each([
    ["first prompt", [], "2026-09-11T16:30:00Z", "day"],
    ["new day", [thursdayPrompt, thursdayReply], "2026-09-11T16:30:00Z", "day"],
    ["quiet hour", [fridayPrompt, fridayReply], "2026-09-11T17:12:04Z", "gap"],
    ["recent activity", [fridayPrompt, fridayReply], "2026-09-11T16:30:00Z", null],
  ] as const)("dates %s identically before and after confirmation", (_name, previous, ts, variant) => {
    const entry = pending("new", ts);
    const before = withTranscriptTimeRows(rows(previous), context(previous, { pendingSends: [entry] }));
    const confirmed = [...previous, message("new", 10, ts, "user")];
    const after = withTranscriptTimeRows(rows(confirmed), context(confirmed, { pendingSends: [entry] }));
    expect(keys(before)).toEqual(keys(after));
    const marker = before.find((item) => item.kind === "time_marker" && item.anchorMessageId === "new");
    expect(marker && marker.kind === "time_marker" ? marker.variant : null).toBe(variant);
    expect(after.filter((item) => item.key === "new")).toHaveLength(1);
    expect(after.find(item => item.key === "new")).not.toHaveProperty("pending");
  });

  it("dates multiple pending sends in order without repeating the day", () => {
    const entries = [pending("one", "2026-09-11T16:30:00Z"), pending("two", "2026-09-11T16:30:01Z")];
    expect(keys(withTranscriptTimeRows([], context([], { pendingSends: entries })))).toEqual([
      "time:day:one", "one", "turn-tail:one", "two", "turn-tail:two",
    ]);
    // Failure or cancellation moves the day label to the next surviving seat.
    expect(keys(withTranscriptTimeRows([], context([], { pendingSends: entries.slice(1) })))).toEqual([
      "time:day:two", "two", "turn-tail:two",
    ]);
    expect(withTranscriptTimeRows([], context([]))).toEqual([]);
  });

  it("reconciles consecutive sends independently without duplicating or moving their date rows", () => {
    const entries = [pending("one", "2026-09-11T16:30:00Z"), pending("two", "2026-09-11T16:30:01Z")];
    for (const count of [0, 1, 2]) {
      const confirmed = entries.slice(0, count).map((entry, index) =>
        message(entry.operationId, index + 1, new Date(entry.createdAt).toISOString(), "user"),
      );
      const out = withTranscriptTimeRows(rows(confirmed), context(confirmed, { pendingSends: entries }));
      expect(keys(out)).toEqual(["time:day:one", "one", "turn-tail:one", "two", "turn-tail:two"]);
      expect(out.filter((item) => item.kind === "pending_user")).toHaveLength(2 - count);
    }
  });

  it("lets host admission add the quiet marker if it crosses the hour threshold", () => {
    const previous = [fridayPrompt, fridayReply];
    const entries = [pending("new", "2026-09-11T17:12:03Z")];
    const before = withTranscriptTimeRows(rows(previous), context(previous, { pendingSends: entries }));
    expect(keys(before)).not.toContain("time:gap:new");
    const confirmed = [...previous, message("new", 10, "2026-09-11T17:12:04Z", "user")];
    const after = withTranscriptTimeRows(rows(confirmed), context(confirmed, { pendingSends: entries }));
    expect(keys(after)).toContain("time:gap:new");
  });

  it("includes settled work in the quiet gap without moving its tail onto a pending prompt", () => {
    const previous = [fridayPrompt, fridayReply];
    const out = withTranscriptTimeRows(rows(previous), context(previous, {
      turnClocks: { u2: clock("u2", "2026-09-11T17:00:00Z") },
      pendingSends: [pending("new", "2026-09-11T17:30:00Z")],
    }));
    expect(keys(out)).toEqual(["time:day:u2", "u2", "a2", "turn-tail:u2", "new", "turn-tail:new"]);
  });

  it("preserves the furthest day and activity instant across backward timestamps", () => {
    const previous = [fridayPrompt, message("late", 5, thursdayReply.created_at, "assistant")];
    const out = withTranscriptTimeRows(rows(previous), context(previous, {
      pendingSends: [pending("new", "2026-09-11T16:30:00Z")],
    }));
    expect(keys(out)).toEqual(["time:day:u2", "u2", "late", "turn-tail:u2", "new", "turn-tail:new"]);
  });

  it("uses the host calendar day when delayed admission crosses midnight", () => {
    const previous = [fridayPrompt, fridayReply];
    const entries = [pending("new", "2026-09-12T06:59:59Z")];
    const before = withTranscriptTimeRows(rows(previous), context(previous, { pendingSends: entries }));
    expect(keys(before)).toContain("time:gap:new");
    const confirmed = [...previous, message("new", 10, "2026-09-12T07:00:01Z", "user")];
    const after = withTranscriptTimeRows(rows(confirmed), context(confirmed, { pendingSends: entries }));
    expect(keys(after)).toContain("time:day:new");
    expect(keys(after)).not.toContain("time:gap:new");
  });
});
