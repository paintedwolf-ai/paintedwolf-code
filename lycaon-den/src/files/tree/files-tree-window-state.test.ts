import { afterEach, describe, expect, it } from "vitest";
import {
  filesTreeClaimed,
  filesTreeCollapsed,
  filesTreeStartsCollapsed,
  resetFilesTreeWindowStateForTests,
  setFilesTreeClaimed,
  setFilesTreeCollapsed,
} from "./files-tree-window-state.ts";

afterEach(resetFilesTreeWindowStateForTests);

describe("files tree window state", () => {
  it("starts detached file windows with the navigator closed", () => {
    expect(
      filesTreeStartsCollapsed({
        kind: "file",
        projectId: "p1",
        rootId: "r1",
        path: "src/main.ts",
        viewId: "v1",
      }),
    ).toBe(true);
    expect(
      filesTreeStartsCollapsed({
        kind: "session",
        projectId: "p1",
        sessionId: "s1",
        viewId: "v1",
      }),
    ).toBe(false);
  });

  it("keeps the navigator visibility in the current window only", () => {
    expect(filesTreeCollapsed()).toBe(false);
    setFilesTreeCollapsed(true);
    expect(filesTreeCollapsed()).toBe(true);
  });

  it("keeps an explicit Show claim until the next resize clears it", () => {
    expect(filesTreeClaimed()).toBe(false);
    setFilesTreeClaimed(true);
    expect(filesTreeClaimed()).toBe(true);
    resetFilesTreeWindowStateForTests();
    expect(filesTreeClaimed()).toBe(false);
  });
});
