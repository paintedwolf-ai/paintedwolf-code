import { describe, expect, it } from "vitest";
import {
  nextRecentSession,
  recordRecentActivity,
  registerRecent,
} from "./recents-store.ts";
import { RECENTS_CAP, type RecentSession } from "../../shared/app-state-types.ts";

const row = (
  sessionId: string,
  lastActivityAt?: number,
  projectId = "p1",
): RecentSession => ({
  projectId,
  sessionId,
  title: sessionId,
  ...(lastActivityAt != null ? { lastActivityAt } : {}),
});

describe("registerRecent", () => {
  it("appends new sessions without reordering existing rows", () => {
    const base = [row("1", 100), row("2", 200)];
    const next = registerRecent(base, {
      projectId: "p1",
      sessionId: "3",
      title: "three",
    });
    expect(next.map((r) => r.sessionId)).toEqual(["1", "2", "3"]);
    expect(next[2]?.lastActivityAt).toBeUndefined();
  });

  it("updates title in place without changing order or activity time", () => {
    const base = [row("1", 100), row("2", 200)];
    const next = registerRecent(base, {
      projectId: "p1",
      sessionId: "2",
      title: "Renamed",
    });
    expect(next.map((r) => r.sessionId)).toEqual(["1", "2"]);
    expect(next[1]?.title).toBe("Renamed");
    expect(next[1]?.lastActivityAt).toBe(200);
  });

  it("keeps the newly registered session when the list is at cap", () => {
    const base = Array.from({ length: RECENTS_CAP }, (_, i) => row(`s${i}`, i));
    const next = registerRecent(base, {
      projectId: "p1",
      sessionId: "fresh",
      title: "Fresh",
    });
    expect(next).toHaveLength(RECENTS_CAP);
    expect(next.some((r) => r.sessionId === "fresh")).toBe(true);
    expect(next.some((r) => r.sessionId === `s${RECENTS_CAP - 1}`)).toBe(false);
  });
});

describe("nextRecentSession", () => {
  it("returns the most-recent remaining session in the same project", () => {
    const recents = [row("a", 300), row("b", 200), row("c", 100)];
    expect(nextRecentSession(recents, { projectId: "p1", sessionId: "a" })?.sessionId).toBe("b");
  });

  it("skips sessions from other projects", () => {
    const recents = [row("a", 300), row("x", 250, "p2"), row("b", 200)];
    expect(nextRecentSession(recents, { projectId: "p1", sessionId: "a" })?.sessionId).toBe("b");
  });

  it("returns null when the project has no other session", () => {
    const recents = [row("a", 300), row("x", 250, "p2")];
    expect(nextRecentSession(recents, { projectId: "p1", sessionId: "a" })).toBeNull();
  });
});

describe("recordRecentActivity", () => {
  it("moves an existing session to the front and bumps activity time", () => {
    const base = [row("1", 100), row("2", 200), row("3", 300)];
    const next = recordRecentActivity(base, {
      projectId: "p1",
      sessionId: "2",
    });
    expect(next.map((r) => r.sessionId)).toEqual(["2", "1", "3"]);
    expect(next[0]?.lastActivityAt).toBeGreaterThan(200);
  });

  it("inserts a new row at the front when activity is recorded first", () => {
    const next = recordRecentActivity([], {
      projectId: "p1",
      sessionId: "1",
      title: "Prompt title",
    });
    expect(next).toEqual([
      expect.objectContaining({
        sessionId: "1",
        title: "Prompt title",
        lastActivityAt: expect.any(Number),
      }),
    ]);
  });
});
