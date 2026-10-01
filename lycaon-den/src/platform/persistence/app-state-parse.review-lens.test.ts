import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState reviewLens", () => {
  it("round-trips per-project comparison and off state", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      reviewLens: {
        byProject: {
          p1: { comparison: "commit", comparisonOff: true, touchedAt: 20 },
          p2: { comparison: "turn", touchedAt: 10 },
        },
      },
    });

    expect(state.reviewLens?.byProject.p1).toEqual({
      comparison: "commit",
      comparisonOff: true,
      touchedAt: 20,
    });
    expect(state.reviewLens?.byProject.p2).toEqual({
      comparison: "turn",
      touchedAt: 10,
    });
  });

  it("keeps whose edits are marked and how removed lines show", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      reviewLens: {
        byProject: {
          p1: { comparison: "new", markMyEdits: true, deletedLines: "inplace", touchedAt: 3 },
          p2: { comparison: "new", markMyEdits: "yes", deletedLines: "sideways", touchedAt: 2 },
        },
      },
    });

    expect(state.reviewLens?.byProject.p1).toEqual({
      comparison: "new",
      markMyEdits: true,
      deletedLines: "inplace",
      touchedAt: 3,
    });
    expect(state.reviewLens?.byProject.p2).toEqual({
      comparison: "new",
      touchedAt: 2,
    });
  });

  it("drops malformed project and comparison rows, including a turn that named a chat", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      reviewLens: {
        byProject: {
          "": { comparison: "commit", touchedAt: 2 },
          bad: { comparison: "mystery", comparisonOff: true, touchedAt: 3 },
          named: { comparison: "turn:s1,3", touchedAt: 5 },
          good: { comparison: " pin:pin-1 ", comparisonOff: "yes", touchedAt: 4 },
        },
      },
    });

    expect(state.reviewLens?.byProject).toEqual({
      good: { comparison: "pin:pin-1", touchedAt: 4 },
    });
  });
});
