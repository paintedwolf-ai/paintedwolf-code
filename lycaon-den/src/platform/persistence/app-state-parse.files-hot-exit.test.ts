import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState filesHotExit", () => {
  it("preserves hot-exit buffer metadata through parse", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      filesHotExit: {
        byProject: {
          p1: {
            buffers: [
              {
                rootId: "r1",
                path: "a.ts",
                kind: "text",
                pinned: true,
                preview: true,
              },
            ],
            activeIndex: 0,
          },
        },
      },
    });
    expect(state.filesHotExit?.byProject.p1?.buffers[0]?.path).toBe("a.ts");
    expect(state.filesHotExit?.byProject.p1?.buffers[0]?.pinned).toBe(true);
    expect(state.filesHotExit?.byProject.p1?.buffers[0]?.preview).toBe(true);
  });

  it("drops corrupted hot-exit payloads", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      filesHotExit: { byProject: { p1: { buffers: [] } } },
    });
    expect(state.filesHotExit).toBeUndefined();
  });
});
