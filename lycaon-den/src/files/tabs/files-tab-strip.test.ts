import { describe, expect, it } from "vitest";
import {
  clampDragIndexWithinGroup,
  closeTargetKeys,
  multiFileDirtyDialogBody,
  orderWithPinnedGroup,
  splitFileNameForMiddleEllipsis,
} from "./files-tab-strip.ts";

const facts = (
  rows: Array<{ key: string; pinned?: boolean; dirty?: boolean }>,
) => {
  const byKey: Record<string, { pinned: boolean; dirty: boolean }> = {};
  const order: string[] = [];
  for (const r of rows) {
    order.push(r.key);
    byKey[r.key] = {
      pinned: r.pinned ?? false,
      dirty: r.dirty ?? false,
    };
  }
  return { order, byKey };
};

describe("files-tab-strip model", () => {
  it("orders pinned group first preserving relative order", () => {
    const { order, byKey } = facts([
      { key: "a" },
      { key: "b", pinned: true },
      { key: "c" },
      { key: "d", pinned: true },
    ]);
    expect(orderWithPinnedGroup(order, byKey)).toEqual(["b", "d", "a", "c"]);
  });

  it("clamps drag within pinned or unpinned group", () => {
    const { order, byKey } = facts([
      { key: "p1", pinned: true },
      { key: "p2", pinned: true },
      { key: "u1" },
      { key: "u2" },
    ]);
    // Drag p1 toward unpinned — clamps to last pinned slot.
    expect(clampDragIndexWithinGroup(order, byKey, 0, 3)).toBe(1);
    // Drag u2 toward pinned — clamps to first unpinned slot.
    expect(clampDragIndexWithinGroup(order, byKey, 3, 0)).toBe(2);
    expect(clampDragIndexWithinGroup(order, byKey, 2, 3)).toBe(3);
  });

  it("close-target sets spare pinned except close / closeAll", () => {
    const { order, byKey } = facts([
      { key: "p", pinned: true, dirty: true },
      { key: "a", dirty: true },
      { key: "b" },
      { key: "c", dirty: false },
    ]);
    expect(closeTargetKeys("close", order, byKey, "a")).toEqual(["a"]);
    expect(closeTargetKeys("closeOthers", order, byKey, "a")).toEqual(["b", "c"]);
    expect(closeTargetKeys("closeToTheRight", order, byKey, "a")).toEqual(["b", "c"]);
    expect(closeTargetKeys("closeSaved", order, byKey, "a")).toEqual(["b", "c"]);
    expect(closeTargetKeys("closeAll", order, byKey, "a")).toEqual(["p", "a", "b", "c"]);
  });

  it("multi-file dirty dialog caps listed names", () => {
    const names = Array.from({ length: 10 }, (_, i) => `f${i}.ts`);
    expect(multiFileDirtyDialogBody(names, 8)).toEqual({
      listed: names.slice(0, 8),
      andMore: 2,
    });
    expect(multiFileDirtyDialogBody(names.slice(0, 3), 8).andMore).toBe(0);
  });

  it("middle-ellipsis split keeps the extension readable", () => {
    expect(splitFileNameForMiddleEllipsis("project-files-view.tsx")).toEqual({
      start: "project-files-view",
      end: ".tsx",
    });
    expect(splitFileNameForMiddleEllipsis("short.ts")).toEqual({
      start: "short",
      end: ".ts",
    });
    expect(splitFileNameForMiddleEllipsis("README")).toEqual({
      start: "README",
      end: "",
    });
  });
});
