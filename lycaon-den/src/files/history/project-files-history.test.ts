import { describe, expect, it, vi } from "vitest";
import type { SourceHistoryAction } from "../../api/types.ts";
import type { DirtyGuardResult } from "../commands/project-files-lifecycle.ts";
import { applyProjectSourceHistory, retryProjectSourceHistory } from "./project-files-history.ts";

const createAction: SourceHistoryAction = {
  id: "11111111-1111-4111-8111-111111111111",
  label: "Undo create of note.txt",
  kind: "create",
  root_id: "r1",
  path: "note.txt",
  is_dir: false,
  removes_path: true,
};

describe("project file lifecycle history", () => {
  it("guards a removing action and projects the host result", async () => {
    const confirmChange = vi.fn();
    const guardDirtyUnderPath = vi.fn(async () => "proceed" as const);
    const undoProjectSourceHistory = vi.fn(async () => ({
      entry_id: createAction.id,
      root_id: "r1",
      path: "note.txt",
      op: "delete" as const,
      is_dir: false,
    }));
    const response = await applyProjectSourceHistory(
      { undoProjectSourceHistory, redoProjectSourceHistory: vi.fn() },
      "p1",
      "undo",
      createAction,
      { guardDirtyUnderPath, confirmChange },
    );

    expect(response?.op).toBe("delete");
    expect(guardDirtyUnderPath).toHaveBeenCalledWith("r1", "note.txt");
    expect(undoProjectSourceHistory).toHaveBeenCalledWith(
      "p1",
      expect.objectContaining({ expected_entry_id: createAction.id }),
      undefined,
    );
    expect(confirmChange).toHaveBeenCalledWith(
      expect.objectContaining({ root_id: "r1", path: "note.txt", op: "delete" }),
    );
  });

  it("does not call the host when dirty-buffer resolution is cancelled", async () => {
    const undoProjectSourceHistory = vi.fn();
    const result = await applyProjectSourceHistory(
      { undoProjectSourceHistory, redoProjectSourceHistory: vi.fn() },
      "p1",
      "undo",
      createAction,
      {
        guardDirtyUnderPath: vi.fn(
          async (): Promise<DirtyGuardResult> => "cancel",
        ),
        confirmChange: vi.fn(),
      },
    );
    expect(result).toBeNull();
    expect(undoProjectSourceHistory).not.toHaveBeenCalled();
  });
});
it("guards newly dirty buffers before retrying a history operation", async () => {
  const retrySourceOperation = vi.fn();
  const guardDirtyUnderPath = vi.fn(async () => "cancel" as const);
  await retryProjectSourceHistory(
    {
      getProjectSourceHistory: vi.fn(async () => ({ undo: createAction, redo: null })),
      retrySourceOperation,
    },
    "p1",
    "original-operation",
    "undo",
    { guardDirtyUnderPath, confirmChange: vi.fn() },
    createAction.id,
  );
  expect(guardDirtyUnderPath).toHaveBeenCalledWith("r1", "note.txt");
  expect(retrySourceOperation).not.toHaveBeenCalled();
});

it("rejects stale history retries before resolving another action's dirty buffers", async () => {
  const retrySourceOperation = vi.fn();
  const guardDirtyUnderPath = vi.fn();
  await expect(retryProjectSourceHistory(
    {
      getProjectSourceHistory: vi.fn(async () => ({ undo: createAction, redo: null })),
      retrySourceOperation,
    },
    "p1",
    "original-operation",
    "undo",
    { guardDirtyUnderPath, confirmChange: vi.fn() },
    "earlier-history-entry",
  )).rejects.toThrow("File history changed");
  expect(guardDirtyUnderPath).not.toHaveBeenCalled();
  expect(retrySourceOperation).not.toHaveBeenCalled();
});
