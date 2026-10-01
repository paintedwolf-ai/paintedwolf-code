import { expect, it, vi } from "vitest";
import { FilesTreeViewport, treeViewportAnchor } from "./files-tree-viewport.ts";
import { buildPagedTreeModel } from "./files-tree-paged-model.ts";
import { TREE_ROOTS, treeRow } from "../../test/source-tree-view-fixture.ts";

it("anchors before a known row to fill a viewport that straddles a missing page", () => {
  const model = buildPagedTreeModel(TREE_ROOTS, 1_000, [{ kind: "tree", view_id: "view", intent_revision: "intent", projection_revision: "projection",
    span: { start: 80, end: 200 }, extent: { rows: 1_000, complete: false }, anchor: { root_id: "r1", path: "file-80" }, ancestors: [],
    rows: Array.from({ length: 120 }, (_, i) => treeRow(`file-${80 + i}`)) }]);
  expect(treeViewportAnchor(model, 50)).toEqual({ address: { root_id: "r1", path: "file-80" }, before: 30, offset: 0 });
  expect(treeViewportAnchor(model, 95)).toEqual({ address: { root_id: "r1", path: "file-95" }, before: 0, offset: 0 });
  expect(treeViewportAnchor(model, 300)).toBeUndefined();
});

it("keeps a pending page alive while scrolling through overlapping windows", async () => {
  const prefetch = vi.fn(async (_start: number, _signal: AbortSignal) => true);
  const reader = new FilesTreeViewport({ prefetch, updateViewport: vi.fn(), clearViewport: vi.fn() });
  reader.seek(0, 80, 10_000, "1");
  await expect.poll(() => prefetch.mock.calls.map(call => call[0])).toEqual([0, 200, 400, 600]);
  const first = prefetch.mock.calls[0]![1];
  for (let start = 1; start < 100; start++) reader.seek(start, start + 80, 10_000, "1");
  expect(prefetch).toHaveBeenCalledTimes(4);
  expect(first.aborted).toBe(false);
  reader.seek(150, 230, 10_000, "1");
  await expect.poll(() => prefetch.mock.calls.at(-1)?.[0]).toBe(800);
  expect(first.aborted).toBe(false);
  reader.clear();
  expect(prefetch.mock.calls.every(call => call[1].aborted)).toBe(true);
});

it("prioritizes a distant seek, bounds speculation, and fences replaced projections", async () => {
  const prefetch = vi.fn(async (_start: number, _signal: AbortSignal) => true);
  const reader = new FilesTreeViewport({ prefetch, updateViewport: vi.fn(), clearViewport: vi.fn() });
  reader.seek(0, 80, 1_000_000, "1");
  await expect.poll(() => prefetch.mock.calls.length).toBe(4);
  const previous = prefetch.mock.calls.map(call => call[1]);
  prefetch.mockClear();
  reader.seek(900_010, 900_100, 1_000_000, "1");
  expect(previous.every(signal => signal.aborted)).toBe(true);
  await expect.poll(() => prefetch.mock.calls.map(call => call[0])).toEqual([900_000, 900_200, 899_800, 900_400, 899_600, 900_600, 899_400]);
  const held = prefetch.mock.calls.map(call => call[1]);
  prefetch.mockClear();
  reader.seek(900_010, 900_100, 1_000_000, "2");
  expect(held.every(signal => signal.aborted)).toBe(true);
  await expect.poll(() => prefetch.mock.calls.length).toBe(7);
  reader.clear();
});

it("uses the deepest confirmed ancestor for a large scroll and falls back to its root outside that subtree", () => {
  const model = buildPagedTreeModel(TREE_ROOTS, 10_000, [{ kind: "tree", view_id: "view", intent_revision: "intent", projection_revision: "projection",
    span: { start: 101, end: 102 }, extent: { rows: 10_000, complete: false }, anchor: { root_id: "r1", path: "src/a" },
    ancestors: [ { index: 0, end: 10_000, row: treeRow(".", "directory", true) },
      { index: 100, end: 5_000, row: treeRow("src", "directory", true) } ], rows: [treeRow("src/a")] }]);
  expect(treeViewportAnchor(model, 2_000)).toEqual({ address: { root_id: "r1", path: "src" }, before: 0, offset: 1_900 });
  expect(treeViewportAnchor(model, 6_000)).toEqual({ address: { root_id: "r1", path: "." }, before: 0, offset: 6_000 });
});


it("stops speculation after a rejected basis without starting a refresh loop", async () => {
  const prefetch = vi.fn(async () => false);
  const reader = new FilesTreeViewport({ prefetch, updateViewport: vi.fn(), clearViewport: vi.fn() });
  reader.seek(1_000, 1_100, 20_000, "one");
  await expect.poll(() => prefetch.mock.calls.length).toBe(1);
  for (let at = 1_000; at < 1_200; at++) reader.seek(at, at + 100, 20_000, "one");
  await Promise.resolve();
  expect(prefetch).toHaveBeenCalledTimes(1);
  reader.seek(1_200, 1_300, 20_000, "two");
  await expect.poll(() => prefetch.mock.calls.length).toBe(2);
  reader.clear();
});

it("keeps a failed read-ahead silent and resumes after the retry delay", async () => {
  vi.useFakeTimers();
  try {
    const prefetch = vi.fn().mockRejectedValueOnce(new Error("limited")).mockResolvedValue(true);
    const reader = new FilesTreeViewport({ prefetch, updateViewport: vi.fn(), clearViewport: vi.fn() });
    reader.seek(0, 100, 200, "one");
    await vi.advanceTimersByTimeAsync(0);
    expect(prefetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(4_999);
    expect(prefetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(prefetch).toHaveBeenCalledTimes(2);
    reader.clear();
  } finally { vi.useRealTimers(); }
});
