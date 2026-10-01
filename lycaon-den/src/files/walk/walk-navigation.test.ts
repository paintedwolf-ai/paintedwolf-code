import { describe, expect, it } from "vitest";
import { walkEffectFixture, walkTurnFixture } from "./walk-fixtures.ts";
import { buildWalk } from "./walk-model.ts";
import { searchWalkNavigation, walkNavigation } from "./walk-navigation.ts";

describe("walk history navigation", () => {
  const effects = [
    { ...walkEffectFixture("a", 1, 1), path: "src/auth.ts" },
    { ...walkEffectFixture("b", 2, 2), path: "src/history.ts", op: "rename" as const, from_path: "src/old-history.ts" },
  ];
  const walk = buildWalk("session:s1", [{ file_id: "file-a", root_id: "r1", path: "a.ts", changed_since_presented: false, unpresented_agent_effects: 0, tip: { state: "content", sha256: "tip" }, head_match: "unknown", effects }], [], [], [
    { ...walkTurnFixture(1), prompt: "Fix sign in" },
    { ...walkTurnFixture(2), prompt: "Add keyboard navigation" },
  ]);

  it("offers recorded prompts with chronological chapter ranges", () => {
    expect(walkNavigation(walk).chapters.map(({ title, prompt, start, end }) => ({ title, prompt, start, end }))).toEqual([
      { title: "Turn 1", prompt: "Fix sign in", start: 0, end: 0 },
      { title: "Turn 2", prompt: "Add keyboard navigation", start: 1, end: 1 },
    ]);
  });

  it("finds a change by prompt, path, and previous path without knowing a step number", () => {
    const { entries } = walkNavigation(walk);
    expect(searchWalkNavigation(entries, "KEYBOARD navigation").map((entry) => entry.index)).toEqual([1]);
    expect(searchWalkNavigation(entries, "auth.ts").map((entry) => entry.index)).toEqual([0]);
    expect(searchWalkNavigation(entries, "old-history").map((entry) => entry.index)).toEqual([1]);
    expect(searchWalkNavigation(entries, "no-such-file")).toEqual([]);
    expect(searchWalkNavigation(entries, "  ")).toBe(entries);
  });
});
