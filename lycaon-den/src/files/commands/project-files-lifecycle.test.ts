import { filesBufferText } from "../documents/project-files-buffers.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { scheduleFilesDraftSync } from "../documents/files-draft-sync.ts";
import {
  deleteSourcePath,
  nextDuplicatePath,
  renameSourcePath,
  trashConfirmBody,
} from "./project-files-lifecycle.ts";

const PROJECT = "p1";

function clientMock() {
  return {
    renameProjectSource: vi.fn(async (_pid, req) => ({
      root_id: req.root_id,
      path: req.to,
    })),
    copyProjectSource: vi.fn(async (_pid, req) => ({
      root_id: req.root_id,
      path: req.to,
    })),
    deleteProjectSource: vi.fn(async () => undefined),
    retrySourceOperation: vi.fn(async () => undefined),
  };
}

describe("nextDuplicatePath", () => {
  it("picks copy suffixes until unoccupied", () => {
    const occupied = new Set(["notes.md", "notes copy.md"]);
    expect(nextDuplicatePath("notes.md", occupied)).toBe("notes copy 2.md");
  });
});

describe("trashConfirmBody", () => {
  it("names the file and explains recovery", () => {
    expect(trashConfirmBody("notes.md", false)).toContain('"notes.md"');
    expect(trashConfirmBody("notes.md", false)).toContain("undo");
  });
  it("includes all folder contents without claiming a partial loaded count", () => {
    expect(trashConfirmBody("src", true)).toContain('"src" and its contents');
  });
});

describe("renameSourcePath", () => {
  beforeEach(() => resetProjectFilesForTests());

  it("retargets a dirty buffer to the canonical path", async () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "old.ts",
    });
    applyFilesBufferLoad(PROJECT, key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "old.ts",
      content: "base",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 4,
      sha256: "abc",
    });
    applyFilesBufferDraft(PROJECT, key, "edited");
    scheduleFilesDraftSync(PROJECT, key, () => {
      applyFilesBufferDraft(PROJECT, key, "latest keystroke");
    });

    const confirmChange = vi.fn();
    const c = clientMock();
    await renameSourcePath(
      c,
      PROJECT,
      { rootId: "r1", from: "old.ts", to: "new.ts", isDir: false },
      {
        guardDirtyUnderPath: async () => "proceed",
        confirmChange,
        rootLabelFor: () => "repo",
      },
    );

    const buf = projectFilesState(PROJECT).byKey[`path:r1\u0000new.ts`];
    expect(buf?.path).toBe("new.ts");
    expect(buf?.dirty).toBe(true);
    expect(filesBufferText(buf!)).toBe("latest keystroke");
    expect(confirmChange).toHaveBeenCalledWith(expect.objectContaining({
      root_id: "r1",
      path: "new.ts",
      from_path: "old.ts",
      op: "rename",
      is_dir: false,
    }));
  });
});

describe("deleteSourcePath", () => {
  beforeEach(() => resetProjectFilesForTests());

  it("cancels before the request when the guard returns cancel", async () => {
    const c = clientMock();
    const result = await deleteSourcePath(
      c,
      PROJECT,
      { operationId: "delete-operation", rootId: "r1", path: "gone.ts", recursive: false, isDir: false },
      {
        guardDirtyUnderPath: async () => "cancel",
        confirmChange: vi.fn(),
        rootLabelFor: () => "repo",
      },
    );
    expect(result).toEqual({ ok: false, cancelled: true });
    expect(c.deleteProjectSource).not.toHaveBeenCalled();
  });

  it("maps API errors", async () => {
    const c = clientMock();
    c.deleteProjectSource.mockRejectedValue(
      new LycaonApiError("Trash unavailable", 500, "source_trash_failed", {retryable: true}),
    );
    const result = await deleteSourcePath(
      c,
      PROJECT,
      { operationId: "delete-operation", rootId: "r1", path: "gone.ts", recursive: false, isDir: false },
      {
        guardDirtyUnderPath: async () => "proceed",
        confirmChange: vi.fn(),
        rootLabelFor: () => "repo",
      },
    );
    expect(result).toEqual({
      ok: false,
      code: "source_trash_failed",
      message: "Trash unavailable",
      retryable: true,
      suggestedAction: undefined,
    });
  });
});


it("replays a failed delete with the same operation identity", async () => {
  const c = clientMock();
  c.deleteProjectSource.mockRejectedValueOnce(new Error("Connection lost"));
  const request = { operationId: "stable-delete", rootId: "r1", path: "gone.ts", recursive: false, isDir: false };
  const hooks = { guardDirtyUnderPath: async () => "proceed" as const, confirmChange: vi.fn(), rootLabelFor: () => "repo" };
  expect(await deleteSourcePath(c, PROJECT, request, hooks)).toMatchObject({ok: false, retryable: true});
  expect(hooks.confirmChange).not.toHaveBeenCalled();
  expect(await deleteSourcePath(c, PROJECT, { ...request, retry: true }, hooks)).toEqual({ok: true});
  expect(c.deleteProjectSource).toHaveBeenNthCalledWith(1, PROJECT, "gone.ts", expect.objectContaining({operationId: "stable-delete"}));
  expect(c.deleteProjectSource).toHaveBeenCalledOnce();
  expect(c.retrySourceOperation).toHaveBeenCalledWith(PROJECT, "stable-delete");
  expect(hooks.confirmChange).toHaveBeenCalledOnce();
});

it("retries a move through its accepted operation identity", async () => {
  const retrySourceOperation = vi.fn(async () => ({ root_id: "r1", path: "moved" }));
  const client = { ...clientMock(), retrySourceOperation };
  const confirmChange = vi.fn();
  await renameSourcePath(client, PROJECT,
    { rootId: "r1", from: "source", to: "moved", isDir: true, operationId: "accepted-move", retry: true },
    { guardDirtyUnderPath: async () => "proceed", confirmChange, rootLabelFor: () => "repo" });
  expect(client.renameProjectSource).not.toHaveBeenCalled();
  expect(retrySourceOperation).toHaveBeenCalledWith(PROJECT, "accepted-move");
  expect(confirmChange).toHaveBeenCalledWith(expect.objectContaining({ op: "rename", from_path: "source", path: "moved" }));
});
