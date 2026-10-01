import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState filesTreeView", () => {
  it("round-trips per-window scroll", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      filesTreeView: {
        byWindow: {
          main: { byProject: { p1: { scrollTop: 120 } } },
          "context:files:1": { byProject: { p1: { scrollTop: 40 } } },
        },
      },
    });
    expect(state.filesTreeView?.byWindow?.main?.byProject.p1?.scrollTop).toBe(
      120,
    );
    expect(
      state.filesTreeView?.byWindow?.["context:files:1"]?.byProject.p1
        ?.scrollTop,
    ).toBe(40);
  });

  it("drops non-positive scroll", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      filesTreeView: {
        byWindow: {
          main: {
            byProject: {
              p1: { scrollTop: 0 },
              p2: { scrollTop: -3 },
              p3: { scrollTop: 8 },
            },
          },
        },
      },
    });
    expect(state.filesTreeView?.byWindow?.main?.byProject.p1).toBeUndefined();
    expect(state.filesTreeView?.byWindow?.main?.byProject.p2).toBeUndefined();
    expect(state.filesTreeView?.byWindow?.main?.byProject.p3?.scrollTop).toBe(8);
  });

  it("omits an empty slice", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      filesTreeView: { byWindow: {} },
    });
    expect(state.filesTreeView).toBeUndefined();
  });
});
