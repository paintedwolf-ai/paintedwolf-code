import { describe, expect, it } from "vitest";

import { readHostCorpus } from "../../../test/host-corpus.ts";
import type { EventEnvelope, MessageEvent, SessionBootstrap } from "../../../api/types.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { applyMessageEvent } from "./message-events.ts";

const corpus = readHostCorpus();

/** A chat's transcript the host answered before and after its message events. */
function bootstrapPair(): [SessionBootstrap, SessionBootstrap] {
  const reads = corpus.exchanges.filter(ex => ex.operation === "getSessionBootstrap" && ex.status === 200);
  const [first, last] = reads;
  if (reads.length !== 2 || !first || !last) {
    throw new Error(`the corpus holds ${reads.length} bootstraps; it needs a chat's before and after the sweep`);
  }
  return [first.body as SessionBootstrap, last.body as SessionBootstrap];
}

/** The chat's message events published between the two bootstraps. */
function messageEventsBetween(sessionId: string, from: string, to: string): MessageEvent[] {
  const cursors = corpus.events.map(env => env.cursor);
  const start = cursors.indexOf(from);
  const end = cursors.indexOf(to);
  expect(start, "the first bootstrap's cursor names a published event").toBeGreaterThanOrEqual(0);
  expect(end, "the last bootstrap's cursor names a published event").toBeGreaterThan(start);
  return corpus.events
    .slice(start + 1, end + 1)
    .filter((env): env is Extract<EventEnvelope, { topic: "message" }> => env.topic === "message")
    .map(env => env.data)
    .filter(event => event.session_id === sessionId);
}

/** Live affordances the host stamps on a read; they are not row state. */
const runtimeStamped = new Set(["status", "generating_tokens"]);

/** Each row's stored fields, as the host's last bootstrap names them. */
function rowState(rows: readonly object[], fields: readonly object[]) {
  return rows.map((row, index) =>
    Object.fromEntries(Object.keys(fields[index] ?? row)
      .filter(key => !runtimeStamped.has(key))
      .map(key => [key, (row as Record<string, unknown>)[key]])));
}

describe("host corpus transcript fold", () => {
  it("folds the host's message events onto its first bootstrap into its last", () => {
    const [first, last] = bootstrapPair();
    const sessionId = first.session.id;
    const store = createAppStore();
    store.actions.setCurrentSession(first.session);
    store.actions.installTranscriptBaseline(sessionId, first.transcript.messages, first.transcript.watermark);
    for (const event of messageEventsBetween(sessionId, first.event_cursor, last.event_cursor)) {
      applyMessageEvent(store, event);
    }
    const host = last.transcript.messages;
    expect(rowState(store.state.messages, host)).toEqual(rowState(host, host));
  });
});
