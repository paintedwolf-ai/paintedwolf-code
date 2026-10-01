// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { watchWindowExitForFilesTreeView } from "./files-tree-view-window.ts";

const flushFilesTreeViewToDisk = vi.hoisted(() =>
  vi.fn().mockResolvedValue(undefined),
);

vi.mock("./files-tree-view-state.ts", () => ({
  flushFilesTreeViewToDisk: (...args: unknown[]) =>
    flushFilesTreeViewToDisk(...args),
}));

describe("watchWindowExitForFilesTreeView", () => {
  afterEach(() => {
    flushFilesTreeViewToDisk.mockClear();
  });

  it("flushes on pagehide", () => {
    const dispose = watchWindowExitForFilesTreeView();
    window.dispatchEvent(new Event("pagehide"));
    expect(flushFilesTreeViewToDisk).toHaveBeenCalledOnce();
    dispose();
  });

  it("flushes when the document becomes hidden", () => {
    const dispose = watchWindowExitForFilesTreeView();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(flushFilesTreeViewToDisk).toHaveBeenCalledOnce();
    dispose();
  });
});
