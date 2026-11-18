import { describe, expect, it } from "vitest";

import { readHostCorpus } from "../../../api/testdata/host-corpus.ts";
import type { EventEnvelope, MessageEvent, SessionBootstrap } from "../../../api/types.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { applyMessageEvent } from "./message-events.ts";

const corpus = readHostCorpus();

/** A chat's transcript the host answered before and after its message events. */
function bootstrapPair(): [SessionBootstrap, SessionBootstrap] {
  const reads = corpus.exchanges.filter(ex => ex.operation === "getSessionBootstrap" && ex.status === 200);
  expect(reads.length, "the corpus holds a chat's bootstrap before and after the sweep").toBe(2);
  return [reads[0].body as SessionBootstrap, reads[1].body as SessionBootstrap];
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

/** Each folded row, read through the fields the host's last bootstrap answers. */
function asHostRows(folded: readonly object[], host: readonly object[]) {
  return folded.map((row, index) =>
    Object.fromEntries(Object.keys(host[index] ?? row).map(key => [key, (row as Record<string, unknown>)[key]])));
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
    expect(asHostRows(store.state.messages, last.transcript.messages)).toEqual(last.transcript.messages);
  });
});
