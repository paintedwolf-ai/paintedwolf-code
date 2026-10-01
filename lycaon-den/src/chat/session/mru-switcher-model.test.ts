import { describe, expect, it } from "vitest";
import type { RecentSession } from "../../../shared/app-state-types.ts";
import {
  MRU_SWITCHER_CAP,
  buildMruEntries,
  createMruTracker,
  initialMruIndex,
  nextMruIndex,
} from "./mru-switcher-model.ts";

function recent(
  sessionId: string,
  projectId = "p1",
  lastActivityAt?: number,
): RecentSession {
  return { projectId, sessionId, title: sessionId, lastActivityAt };
}

const lookup = {
  projectName: (id: string) => (id === "p1" ? "First" : "Second"),
  attention: () => undefined,
};

describe("mru tracker", () => {
  it("puts the most recently visited chat first", () => {
    const mru = createMruTracker();
    mru.visit("p1", "a");
    mru.visit("p1", "b");
    expect(mru.order([recent("a"), recent("b")]).map((r) => r.sessionId)).toEqual([
      "b",
      "a",
    ]);
  });

  it("moves a revisited chat back to the front rather than duplicating it", () => {
    const mru = createMruTracker();
    mru.visit("p1", "a");
    mru.visit("p1", "b");
    mru.visit("p1", "a");
    expect(mru.snapshot()).toEqual(["p1:a", "p1:b"]);
  });

  it("keys on project and session together", () => {
    const mru = createMruTracker();
    mru.visit("p1", "same");
    mru.visit("p2", "same");
    expect(mru.snapshot()).toEqual(["p2:same", "p1:same"]);
  });

  it("leaves unvisited chats in the order supplied", () => {
    const mru = createMruTracker();
    const ordered = mru.order([recent("a"), recent("b"), recent("c")]);
    expect(ordered.map((r) => r.sessionId)).toEqual(["a", "b", "c"]);
  });

  it("ranks every visited chat ahead of every unvisited one", () => {
    const mru = createMruTracker();
    mru.visit("p1", "c");
    expect(
      mru.order([recent("a"), recent("b"), recent("c")]).map((r) => r.sessionId),
    ).toEqual(["c", "a", "b"]);
  });

  it("drops a forgotten chat from the stack", () => {
    const mru = createMruTracker();
    mru.visit("p1", "a");
    mru.forget("p1", "a");
    expect(mru.snapshot()).toEqual([]);
  });
});

describe("buildMruEntries", () => {
  it("decorates rows with project name and caps the list", () => {
    const mru = createMruTracker();
    const rows = Array.from({ length: MRU_SWITCHER_CAP + 3 }, (_, i) =>
      recent(`s${i}`),
    );
    const entries = buildMruEntries(mru, rows, lookup);
    expect(entries).toHaveLength(MRU_SWITCHER_CAP);
    expect(entries[0]?.projectName).toBe("First");
  });

  it("falls back to a placeholder for an untitled chat", () => {
    const mru = createMruTracker();
    const entries = buildMruEntries(
      mru,
      [{ projectId: "p1", sessionId: "s", title: "   " }],
      lookup,
    );
    expect(entries[0]?.title).toBe("Untitled chat");
  });
});

describe("switcher index", () => {
  // Index 0 is the current chat; opening there would make one press a no-op.
  it("opens on the previous chat, not the current one", () => {
    expect(initialMruIndex(5, 1)).toBe(1);
  });

  it("opens on the oldest chat when stepping backward", () => {
    expect(initialMruIndex(5, -1)).toBe(4);
  });

  it("stays put when there is only one chat to land on", () => {
    expect(initialMruIndex(1, 1)).toBe(0);
    expect(initialMruIndex(0, 1)).toBe(0);
  });

  it("wraps in both directions", () => {
    expect(nextMruIndex(2, 3, 1)).toBe(0);
    expect(nextMruIndex(0, 3, -1)).toBe(2);
  });

  it("never divides by an empty list", () => {
    expect(nextMruIndex(0, 0, 1)).toBe(0);
  });
});
