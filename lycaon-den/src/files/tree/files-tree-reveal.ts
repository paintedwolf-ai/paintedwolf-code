import { filesPaneChrome } from "../components/files-pane-chrome.ts";
import { createEffect, createSignal, onCleanup, untrack, type Accessor } from "solid-js";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { filesTreeDirKey } from "./files-tree-keys.ts";
import { connectSourceTreeWorkspace, type SourceTreeBrowse, type SourceTreeConnection } from "./source-tree-store.ts";

export type FileTreeRevealAction =
  | { onSelect: () => void }
  | { disabled: true; description: string };

type TreeBuffer = Pick<FileBuffer, "kind" | "rootId" | "path" | "jobId" | "sourcePresent">;

/** Reveal eligibility uses the toolbar's address capability. */
export function hasFilesTreeAddress(buffer: TreeBuffer): boolean {
  return filesPaneChrome(buffer.kind).address &&
    !buffer.jobId && Boolean(buffer.rootId && buffer.path);
}

function parentDirectory(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? "." : path.slice(0, slash);
}

/** Shares the tree's membership facts without expanding folders or moving focus. */
export function createFilesTreeReveal(options: {
  projectId: string;
  workspaceId: Accessor<string>;
  available: Accessor<boolean>;
  rootIds: Accessor<readonly string[]>;
  buffers: Accessor<readonly TreeBuffer[]>;
  deletedPaths: (rootId: string) => readonly string[];
  browse: SourceTreeBrowse;
}) {
  const [connection, setConnection] = createSignal<SourceTreeConnection>();
  const [revision, setRevision] = createSignal(0);
  const [failed, setFailed] = createSignal(new Set<string>());

  createEffect(() => {
    const workspaceId = options.workspaceId();
    if (!options.available() || !workspaceId) return;
    const next = connectSourceTreeWorkspace({ projectId: options.projectId, workspaceId, browse: options.browse });
    setConnection(next);
    setFailed(new Set<string>());
    const unsubscribe = next.subscribe(() => setRevision((value) => value + 1));
    onCleanup(() => {
      unsubscribe();
      next.disconnect();
      setConnection(undefined);
    });
  });

  createEffect(() => {
    const current = connection();
    if (!current) return;
    const roots = options.rootIds();
    const directories = new Map<string, { rootId: string; dir: string }>();
    for (const buffer of options.buffers()) {
      if (!hasFilesTreeAddress(buffer) || !roots.includes(buffer.rootId)) continue;
      const dir = parentDirectory(buffer.path);
      directories.set(filesTreeDirKey(buffer.rootId, dir), { rootId: buffer.rootId, dir });
    }
    current.observe(directories.keys(), true);
    let disposed = false;
    onCleanup(() => { disposed = true; });
    untrack(() => {
      for (const [key, address] of directories) {
        void current.load(address.rootId, address.dir).catch(() => {
          if (!disposed) setFailed((previous) => new Set(previous).add(key));
        });
      }
    });
  });

  const unavailable = (buffer: TreeBuffer | null | undefined): string | null => {
    if (!options.available()) return "The file tree is unavailable in this window.";
    if (!buffer || !hasFilesTreeAddress(buffer)) return "This tab has no item in the file tree.";
    if (!options.rootIds().includes(buffer.rootId)) return "The containing folder is unavailable.";
    // Review tombstones are tree items even when the file no longer exists on disk.
    if (options.deletedPaths(buffer.rootId).includes(buffer.path)) return null;
    void revision();
    const dir = parentDirectory(buffer.path);
    const snapshot = connection()?.get(buffer.rootId, dir);
    if (snapshot) {
      const name = buffer.path.slice(buffer.path.lastIndexOf("/") + 1);
      return snapshot.listing.entries.some((entry) => entry.name === name && !entry.is_dir)
        ? null : "This file is not in the current tree.";
    }
    if (buffer.sourcePresent) return null;
    return failed().has(filesTreeDirKey(buffer.rootId, dir))
      ? "The file's tree location is unavailable." : "Checking the file's tree location…";
  };

  return {
    unavailable,
    action: (buffer: Accessor<TreeBuffer | null | undefined>, reveal: (rootId: string, path: string) => void): FileTreeRevealAction => {
      const reason = unavailable(buffer());
      return reason ? { disabled: true, description: reason } : {
        onSelect: () => {
          const current = buffer();
          if (current && !unavailable(current)) reveal(current.rootId, current.path);
        },
      };
    },
  };
}
