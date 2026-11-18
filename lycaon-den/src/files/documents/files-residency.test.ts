import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
// The lifecycle loads first so it binds the mock; the document module imports it back.
import { openFilesBuffer, resetProjectFilesForTests } from "./project-files-buffer-lifecycle.ts";
import { documentResidency } from "./document-residency.ts";
import { evictEditorDocument } from "./editor-document.ts";
import { createFilesResidency } from "./files-residency.ts";

vi.mock("./editor-document.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("./editor-document.ts")>(),
  evictEditorDocument: vi.fn(async () => true),
}));

const PROJECT = "project";

const openPreview = (path: string, documentId: string) =>
  openFilesBuffer(PROJECT, { intent: "transient", rootId: "root", rootLabel: "repo", path, documentId });

const settled = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("files residency", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    documentResidency.reset();
    vi.mocked(evictEditorDocument).mockClear();
  });

  it("keeps a replaced preview's document until its successor is displayed", async () => {
    const first = openPreview("first.ts", "first-document");
    const [displayed, setDisplayed] = createSignal<string | null>(first);
    const dispose = createRoot((dispose) => { createFilesResidency(PROJECT, displayed); return dispose; });
    await settled();

    const second = openPreview("second.ts", "second-document");
    await settled();
    expect(evictEditorDocument).not.toHaveBeenCalled();

    setDisplayed(second);
    await settled();
    expect(evictEditorDocument).toHaveBeenCalledExactlyOnceWith("first-document");
    dispose();
  });

  it("keeps a replaced preview's document a tab has reopened", async () => {
    const first = openPreview("first.ts", "first-document");
    const [displayed, setDisplayed] = createSignal<string | null>(first);
    const dispose = createRoot((dispose) => { createFilesResidency(PROJECT, displayed); return dispose; });
    await settled();

    openPreview("second.ts", "second-document");
    const reopened = openFilesBuffer(PROJECT, { intent: "permanent", rootId: "root", rootLabel: "repo", path: "first.ts", documentId: "first-document" });
    setDisplayed(reopened);
    const third = openFilesBuffer(PROJECT, { intent: "permanent", rootId: "root", rootLabel: "repo", path: "third.ts", documentId: "third-document" });
    setDisplayed(third);
    await settled();
    expect(evictEditorDocument).not.toHaveBeenCalledWith("first-document");
    dispose();
  });
});
