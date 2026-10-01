import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { FILE_BUFFER_KINDS, type FileBufferKind } from "../documents/project-files-buffer-kind.ts";
import { filesPaneChrome } from "./files-pane-chrome.ts";

const PANES_DIR = import.meta.dirname;

/** ReadonlyFilesDocument supplies both toolbar and status chrome. */
const CHROME_HOOKS = {
  toolbar: ["den-files-toolbar", "FilesEditorToolbar", "ReadonlyFilesDocument"],
  address: ["FilesPathCrumbs"],
  status: ["den-files-editor__status", "FilesEditorStatus", "ReadonlyFilesDocument"],
} as const;

/** Every pane the stage composes, and the file it renders from. */
const PANES: Record<string, { kind: FileBufferKind; file: string }> = {
  editor: { kind: "text", file: "../editor/FilesEditor.tsx" },
  image: { kind: "image", file: "../editor/FilesImageViewer.tsx" },
  info: { kind: "info", file: "FilesInfoCard.tsx" },
  chat: { kind: "chat", file: "ChatContentPage.tsx" },
  walk: { kind: "walk", file: "../walk/WalkStepPage.tsx" },
  diffs: { kind: "diffs", file: "../review/DiffsPage.tsx" },
  trust: { kind: "trust", file: "../../components/trust/TrustReviewPage.tsx" },
};

function source(file: string): string {
  return readFileSync(join(PANES_DIR, file), "utf8");
}

function uses(text: string, hooks: readonly string[]): boolean {
  return hooks.some((hook) => text.includes(hook));
}

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

  it.each(Object.entries(PANES))("%s carries only the chrome its kind answers for", (_name, pane) => {
    const text = source(pane.file);
    const chrome = filesPaneChrome(pane.kind);
    expect(uses(text, CHROME_HOOKS.toolbar), `${pane.file} toolbar`).toBe(!chrome.page);
    expect(uses(text, CHROME_HOOKS.address), `${pane.file} breadcrumb`).toBe(chrome.address);
    expect(uses(text, CHROME_HOOKS.status), `${pane.file} status bar`).toBe(chrome.document);
  });
});
