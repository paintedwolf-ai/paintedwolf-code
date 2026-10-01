import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FILE_UNDO_TOAST_MS,
  dismissFileUndoToast,
  getFileUndoToast,
  resetFileUndoToastForTests,
  showFileUndoToast,
  subscribeFileUndoToast,
} from "./file-undo-toast.ts";

afterEach(() => {
  resetFileUndoToastForTests();
});

describe("file-undo-toast", () => {
  it("holds one toast per project and replaces it", () => {
    showFileUndoToast({
      projectId: "p1",
      label: "Restored a.ts",
      undo: [{ kind: "delete", rootId: "r", path: "a.ts" }],
    });
    const first = getFileUndoToast("p1");
    expect(first?.label).toBe("Restored a.ts");
    showFileUndoToast({
      projectId: "p1",
      label: "Rejected hunk in a.ts",
      undo: [
        {
          kind: "put",
          rootId: "r",
          path: "a.ts",
          content: "x\n",
          encoding: "utf-8",
          baseSha256: "s",
        },
      ],
    });
    expect(getFileUndoToast("p1")?.label).toBe("Rejected hunk in a.ts");
    expect(getFileUndoToast("p1")?.id).not.toBe(first?.id);
  });

  it("does not leak a toast across projects", () => {
    showFileUndoToast({
      projectId: "p1",
      label: "Restored a.ts",
      undo: [{ kind: "delete", rootId: "r", path: "a.ts" }],
    });
    expect(getFileUndoToast("p2")).toBeNull();
  });

  it("keeps one project's undo alive while another project acts", () => {
    const p1 = showFileUndoToast({
      projectId: "p1",
      label: "Restored a.ts",
      undo: [{ kind: "delete", rootId: "r", path: "a.ts" }],
    });
    showFileUndoToast({
      projectId: "p2",
      label: "Restored b.ts",
      undo: [{ kind: "delete", rootId: "r", path: "b.ts" }],
    });
    expect(getFileUndoToast("p1")?.id).toBe(p1.id);
    expect(getFileUndoToast("p2")?.label).toBe("Restored b.ts");
  });

  it("expires each project's undo on its own schedule", () => {
    vi.useFakeTimers();
    showFileUndoToast({
      projectId: "p1",
      label: "Restored a.ts",
      undo: [{ kind: "delete", rootId: "r", path: "a.ts" }],
    });
    vi.advanceTimersByTime(FILE_UNDO_TOAST_MS / 2);
    showFileUndoToast({
      projectId: "p2",
      label: "Restored b.ts",
      undo: [{ kind: "delete", rootId: "r", path: "b.ts" }],
    });
    vi.advanceTimersByTime(FILE_UNDO_TOAST_MS / 2 + 1);
    expect(getFileUndoToast("p1")).toBeNull();
    expect(getFileUndoToast("p2")?.label).toBe("Restored b.ts");
    vi.advanceTimersByTime(FILE_UNDO_TOAST_MS / 2);
    expect(getFileUndoToast("p2")).toBeNull();
    vi.useRealTimers();
  });

  it("dismisses and expires", () => {
    vi.useFakeTimers();
    const seen: string[] = [];
    const stop = subscribeFileUndoToast((id) => seen.push(id));
    const toast = showFileUndoToast({
      projectId: "p1",
      label: "Restored a.ts",
      undo: [{ kind: "delete", rootId: "r", path: "a.ts" }],
    });
    dismissFileUndoToast("p1", toast.id);
    expect(getFileUndoToast("p1")).toBeNull();
    showFileUndoToast({
      projectId: "p1",
      label: "Restored b.ts",
      undo: [{ kind: "delete", rootId: "r", path: "b.ts" }],
    });
    vi.advanceTimersByTime(FILE_UNDO_TOAST_MS);
    expect(getFileUndoToast("p1")).toBeNull();
    expect(seen).toContain("p1");
    stop();
    vi.useRealTimers();
  });
});
