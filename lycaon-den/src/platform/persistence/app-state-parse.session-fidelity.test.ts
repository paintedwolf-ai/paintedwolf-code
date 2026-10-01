import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState session fidelity", () => {
  it("round-trips editorViewState and closedBufferRing", () => {
    const key = "root\u0000src/a.ts";
    const state = parseAppState({
      version: 1,
      recents: [],
      editorViewState: {
        byKey: {
          [key]: {
            sha: "abc",
            cursor: { anchor: 2, head: 4 },
            scrollTop: 10,
            folds: [{ from: 0, to: 1 }],
            capturedAt: 99,
          },
        },
      },
      closedBufferRing: {
        byProject: {
          p1: [
            {
              key,
              rootId: "root",
              path: "src/a.ts",
              pinned: true,
            },
          ],
        },
      },
    });
    expect(state.editorViewState?.byKey[key]?.scrollTop).toBe(10);
    expect(state.closedBufferRing?.byProject.p1?.[0]?.path).toBe("src/a.ts");
  });

  it("drops malformed view state and keeps valid ring entries", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      editorViewState: {
        byKey: {
          bad: { sha: "x" },
        },
      },
      closedBufferRing: {
        byProject: {
          p1: [
            {
              key: "r\u0000b.ts",
              rootId: "r",
              path: "b.ts",
            },
          ],
        },
      },
    });
    expect(state.editorViewState?.byKey ?? {}).toEqual({});
    expect(state.closedBufferRing?.byProject.p1).toHaveLength(1);
    expect(state.closedBufferRing?.byProject.p1?.[0]?.path).toBe("b.ts");
  });
});
