import { filesBufferText } from "./project-files-buffers.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SourceChange } from "../../api/types.ts";
import {
  applyBufferSourceChanged as applyBufferSourceChangedImpl,
  applyBufferSourceResync,
  setBufferLiveHandlers,
} from "./buffer-live.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } from "./project-files-buffers.ts";
import { projectFilesState } from "./files-buffer-state.ts";
import { scheduleFilesDraftSync } from "./files-draft-sync.ts";

const PROJECT = "live-p";

function evt(
  partial: Partial<SourceChange> & { op: SourceChange["op"]; path: string },
): SourceChange {
  return {
    root_id: "r1",
    origin: "agent",
    changed_at: new Date().toISOString(),
    ...partial,
  };
}

function applyBufferSourceChanged(projectId: string, event: SourceChange): void {
  applyBufferSourceChangedImpl(projectId, "project", event);
}

function openLoaded(path: string, content: string, sha = "sha-a") {
  const key = openFilesBuffer(PROJECT, {
    intent: "permanent",
    rootId: "r1",
    rootLabel: "repo",
    path,
  });
  applyFilesBufferLoad(PROJECT, key, {
    file_id: "",
    version_id: "",
    workspace_id: "workspace-1",
    workspace_kind: "project",
    path,
    content,
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: content.length,
    sha256: sha,
  });
  return key;
}

describe("buffer-live lifecycle + divergence", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    setBufferLiveHandlers(PROJECT, null);
  });

  it("reconciles retained buffers when Files mounts after a continuity gap", () => {
    openLoaded("src/a.ts", "cached");
    applyBufferSourceResync(PROJECT);
    const onClean = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });
    expect(onClean).toHaveBeenCalledOnce();
    setBufferLiveHandlers(PROJECT, null);
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });
    expect(onClean).toHaveBeenCalledOnce();
  });

  it("clean write notifies onCleanDiskChange", () => {
    const key = openLoaded("src/a.ts", "old");
    const onClean = vi.fn();
    const onDirty = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: onDirty,
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-b" }),
    );
    expect(onClean).toHaveBeenCalledOnce();
    expect(onDirty).not.toHaveBeenCalled();
    expect(projectFilesState(PROJECT).byKey[key]!.diverged).toBe(false);
  });

  it("directory changes refresh clean descendants and reconcile dirty descendants", () => {
    const clean = openLoaded("src/a.ts", "old");
    const dirty = openLoaded("src/deep/b.ts", "old");
    openLoaded("src-other/c.ts", "old");
    applyFilesBufferDraft(PROJECT, dirty, "draft");
    const onClean = vi.fn();
    const onDirty = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: onDirty,
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(PROJECT, evt({ op: "write", path: "src", is_dir: true }));
    expect(onClean.mock.calls.map(([buf]) => buf.key)).toEqual([clean]);
    expect(onDirty.mock.calls.map(([buf]) => buf.key)).toEqual([dirty]);
    expect(filesBufferText(projectFilesState(PROJECT).byKey[dirty]!)).toBe("draft");
  });

  it("leaves a buffer whose first read is still in flight to that read", () => {
    openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/pending.ts",
    });
    const onClean = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });

    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/pending.ts", after_sha256: "sha-b" }),
    );

    expect(onClean).not.toHaveBeenCalled();
  });

  it("dirty writes request host reconciliation without inferring divergence", () => {
    const key = openLoaded("src/a.ts", "old");
    applyFilesBufferDraft(PROJECT, key, "draft");
    const onDirty = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: vi.fn(),
      onDirtyDiskChange: onDirty,
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-b" }),
    );
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-c" }),
    );
    expect(onDirty).toHaveBeenCalledTimes(2);
    expect(projectFilesState(PROJECT).byKey[key]!.diverged).toBe(false);
  });

  it("treats an identical after_sha256 as a clean-buffer invalidation", () => {
    openLoaded("src/a.ts", "old", "sha-a");
    const onClean = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-a" }),
    );
    expect(onClean).toHaveBeenCalledOnce();
  });

  it("reconciles an identical after_sha256 without inferring divergence", () => {
    const key = openLoaded("src/a.ts", "old", "sha-a");
    applyFilesBufferDraft(PROJECT, key, "draft on top of sha-a");
    const onClean = vi.fn();
    const onDirty = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: onDirty,
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-a" }),
    );
    expect(onClean).not.toHaveBeenCalled();
    expect(onDirty).toHaveBeenCalledOnce();
    expect(projectFilesState(PROJECT).byKey[key]!.diverged).toBe(false);
  });

  it("does not use at-least-once events as document state", () => {
    const key = openLoaded("src/a.ts", "old", "sha-a");
    applyFilesBufferDraft(PROJECT, key, "draft");
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: vi.fn(),
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-b" }),
    );
    expect(projectFilesState(PROJECT).byKey[key]!.diverged).toBe(false);
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", after_sha256: "sha-a" }),
    );
    expect(projectFilesState(PROJECT).byKey[key]!.diverged).toBe(false);
  });

  it("uses workspace kind when a project change carries job attribution", () => {
    openLoaded("src/a.ts", "old");
    const onClean = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: "src/a.ts", worker_id: "job-1", after_sha256: "sha-b" }),
    );
    expect(onClean).toHaveBeenCalledOnce();
  });

  it("announces EditorConfig changes for every lifecycle operation", () => {
    const onEditorConfigChange = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: vi.fn(),
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
      onEditorConfigChange,
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "write", path: ".editorconfig" }),
    );
    applyBufferSourceChanged(
      PROJECT,
      evt({ op: "delete", path: "pkg/.editorconfig" }),
    );
    applyBufferSourceChanged(
      PROJECT,
      evt({
        op: "rename",
        from_path: "old/.editorconfig",
        path: "new/.editorconfig",
      }),
    );
    expect(onEditorConfigChange).toHaveBeenCalledTimes(3);
    expect(onEditorConfigChange).toHaveBeenCalledWith("r1");
  });

  it("foreign rename retargets dirty buffer path", () => {
    const key = openLoaded("src/old.ts", "body");
    applyFilesBufferDraft(PROJECT, key, "dirty body");
    scheduleFilesDraftSync(PROJECT, key, () => {
      applyFilesBufferDraft(PROJECT, key, "latest dirty body");
    });
    applyBufferSourceChanged(
      PROJECT,
      evt({
        op: "rename",
        path: "src/new.ts",
        from_path: "src/old.ts",
      }),
    );
    const state = projectFilesState(PROJECT);
    const buf = Object.values(state.byKey).find((b) => b.path === "src/new.ts");
    expect(buf).toBeTruthy();
    expect(buf!.dirty).toBe(true);
    expect(filesBufferText(buf!)).toBe("latest dirty body");
    expect(state.byKey[key]).toBeUndefined();
  });

  it("foreign delete with dirty buffers calls guarded close handler", () => {
    const key = openLoaded("src/a.ts", "body");
    applyFilesBufferDraft(PROJECT, key, "dirty");
    const onForeign = vi.fn();
    setBufferLiveHandlers(PROJECT, {
      onCleanDiskChange: vi.fn(),
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: onForeign,
    });
    applyBufferSourceChanged(PROJECT, evt({ op: "delete", path: "src/a.ts" }));
    expect(onForeign).toHaveBeenCalledOnce();
    expect(projectFilesState(PROJECT).byKey[key]).toBeTruthy();
  });

  it("foreign delete on clean buffer closes it", () => {
    const key = openLoaded("src/a.ts", "body");
    applyBufferSourceChanged(PROJECT, evt({ op: "delete", path: "src/a.ts" }));
    expect(projectFilesState(PROJECT).byKey[key]).toBeUndefined();
  });
});
