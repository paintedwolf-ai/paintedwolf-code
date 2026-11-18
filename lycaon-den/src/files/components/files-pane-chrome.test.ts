import { describe, expect, it } from "vitest";
import { FILE_BUFFER_KINDS, type FileBufferKind } from "../documents/project-files-buffer-kind.ts";
import { filesPaneChrome } from "./files-pane-chrome.ts";

describe("what chrome a stage pane carries", () => {
  it("answers for every buffer kind, so a new pane cannot skip the question", () => {
    for (const kind of FILE_BUFFER_KINDS) {
      expect(filesPaneChrome(kind), kind).toBeDefined();
    }
  });

  it("gives the breadcrumb an address and the status bar a document", () => {
    // A file the editor opens has both.
    expect(filesPaneChrome("text")).toEqual({ address: true, document: true, page: false });
    // A file it cannot open as a document keeps its address and loses the status bar.
    expect(filesPaneChrome("info")).toEqual({ address: true, document: false, page: false });
    // Recorded chat content is a document with nowhere in the tree.
    expect(filesPaneChrome("chat")).toEqual({ address: false, document: true, page: false });
    // All diffs heads itself and reads as one document.
    expect(filesPaneChrome("diffs")).toEqual({ address: false, document: true, page: true });
  });

  it("never heads a tree place as a page, and always heads a pane with neither", () => {
    for (const kind of FILE_BUFFER_KINDS) {
      const chrome = filesPaneChrome(kind);
      if (chrome.address) expect(chrome.page, kind).toBe(false);
      if (!chrome.address && !chrome.document) expect(chrome.page, kind).toBe(true);
    }
  });

  it("gives the toolbar to every pane that is not a page", () => {
    const isPage = (kind: FileBufferKind) => filesPaneChrome(kind).page;
    const pages: FileBufferKind[] = ["walk", "trust", "diffs"];
    const addressed: FileBufferKind[] = ["text", "image", "info", "diff", "chat"];
    expect(pages.every(isPage)).toBe(true);
    expect(addressed.some(isPage)).toBe(false);
  });
});
