import { ROOTS, loadedBuffer, resetProjectFilesViewTest } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { FilesInfoCard } from "./FilesInfoCard.tsx";
import { applyFilesBufferLoadError, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

describe("File info presentation", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => registerNoticePublisher(null));

  const projectNotices = (store: ReturnType<typeof createNoticeStore>) =>
    selectProjectNoticeGroups(store.index()).find(group => group.projectId === "p1")?.notices ?? [];

  it("keeps binary decoding behind an explicit secondary action", async () => {
    const buffer = loadedBuffer({ kind: "info", path: "database.sqlite3" });
    const reopen = vi.fn();
    render(() => <FilesInfoCard projectId="p1" buffer={buffer} roots={ROOTS} onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} onReopenAsUTF16={reopen} />);
    expect(screen.queryByTestId("files-info-open-utf16le-btn")).toBeNull();
    fireEvent.click(screen.getByTestId("files-info-open-text-btn"));
    fireEvent.click(await screen.findByTestId("files-info-open-utf16le-menu"));
    expect(reopen).toHaveBeenCalledWith("utf-16le");
  });

  it("does not call a restored file binary before its read completes", () => {
    const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "Cargo.toml", intent: "permanent", kind: "info" });
    const buffer = projectFilesState("p1").byKey[key]!;
    render(() => <FilesInfoCard projectId="p1" buffer={buffer} roots={ROOTS} onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} />);
    expect(screen.getByTestId("files-info-loading")).toBeTruthy();
    expect(screen.queryByTestId("files-info-explain")).toBeNull();
    expect(screen.queryByTestId("files-info-size")).toBeNull();
    expect(screen.queryByTestId("files-info-open-utf16le-btn")).toBeNull();
    const store = createNoticeStore();
    registerNoticePublisher(store);
    applyFilesBufferLoadError("p1", key, "Permission denied");
    expect(projectNotices(store)).toMatchObject([{ code: "files_document_unavailable", message: "Permission denied" }]);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("files-info-explain")).toBeNull();
  });

  it("shows a read failure instead of stale file classification", () => {
    const buffer = loadedBuffer({ kind: "info" });
    render(() => <FilesInfoCard projectId="p1" buffer={buffer} roots={ROOTS} onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} />);
    expect(screen.getByTestId("files-info-explain").textContent).toContain("binary");
    const store = createNoticeStore();
    registerNoticePublisher(store);
    applyFilesBufferLoadError("p1", buffer.key, "File is unavailable");
    expect(projectNotices(store)).toMatchObject([{ code: "files_document_unavailable", message: "File is unavailable" }]);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("files-info-explain")).toBeNull();
  });
});
