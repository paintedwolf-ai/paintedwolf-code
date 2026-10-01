import { expect, it } from "vitest";
import type { Message, TurnClock } from "../../../api/types.ts";
import { withTranscriptTimeRows } from "../presentation/transcript-time-rows.ts";
import { transcriptRowSeams } from "./transcript-row-seams.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";

it("keeps turn rows and seams fixed through prompt confirmation and clock delivery", () => {
  const ts = "2026-09-17T12:00:00Z";
  const message = (id: string, ord: number, role: Message["role"]): Message => ({
    id, ord, role, created_at: ts, content: id, origin: "user", authority: "user", trust_tier: "trusted",
  });
  const previous = [message("old", 1, "user"), message("reply", 2, "assistant")];
  const pending = { kind: "prompt" as const, operationId: "new", text: "new", state: "sending" as const, createdAt: Date.parse(ts) };
  const oldClock: TurnClock = { session_id: "session", opening_message_id: "old", running: false, settled_at: ts, active_ms: 1000, work_ms: 1000 };
  const layout = (messages: Message[], turnClocks: Record<string, TurnClock>) => {
    const rows: DisplayTranscriptItem[] = messages.map(m => ({ kind: m.role === "user" ? "user" : "assistant", key: m.id, text: m.content }));
    const items = withTranscriptTimeRows(rows, {
      messageById: new Map(messages.map(m => [m.id, m])), turnClocks,
      pendingSends: [pending], dayKey: () => 1,
    });
    const seams = transcriptRowSeams(items);
    return items.map(item => [item.key, seams.get(item.key)]);
  };
  const before = layout(previous, { old: oldClock });
  const confirmed = [...previous, message("new", 3, "user")];
  const clocks = {
    old: oldClock,
    new: { ...oldClock, opening_message_id: "new", running: true, settled_at: undefined },
  };
  expect(layout(confirmed, { old: oldClock })).toEqual(before);
  expect(layout(previous, clocks)).toEqual(before);
  expect(layout(confirmed, clocks)).toEqual(before);
});

it("keeps a reserved queue send in the same turn through its host echo", () => {
  const ts = "2026-09-17T12:00:00Z";
  const opener: Message = {
    id: "opening", ord: 1, role: "user", created_at: ts, content: "Start", origin: "user", authority: "user", trust_tier: "trusted",
  };
  const continuation: Message = { ...opener, id: "continued", ord: 2, kind: "user_continuation", content: "Continue" };
  const pending = { kind: "queue_send" as const, operationId: continuation.id, text: continuation.content, state: "accepted" as const, createdAt: Date.parse(ts) };
  const project = (messages: Message[], waiting: boolean) => {
    const items: DisplayTranscriptItem[] = messages.map(m => ({ kind: "user", key: m.id, text: m.content }));
    return withTranscriptTimeRows(items, {
      messageById: new Map(messages.map(m => [m.id, m])), turnClocks: {},
      pendingSends: waiting ? [pending] : [], dayKey: () => 1,
    });
  };
  const before = project([opener], true);
  const after = project([opener, continuation], false);
  expect(before.map(item => item.key)).toEqual(["time:day:opening", "opening", "continued", "turn-tail:opening"]);
  expect(after.map(item => item.key)).toEqual(before.map(item => item.key));
  expect(transcriptRowSeams(after, { continuation: key => key === continuation.id })).toEqual(transcriptRowSeams(before));
});
