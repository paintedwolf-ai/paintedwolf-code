/** Adapts native paths and web file drops into chat attachments. */

import { longestMatchingRoot, relativeUnderRoot, type ResolveProjectRoot } from "../../api/project-path.ts";
import { mediaMimeFromPath } from "../../chat/composer/composer-attachments.ts";
import { createClaimable } from "../interaction/claimable.ts";
import { hostSharesDevice } from "../connection/host-identity.ts";
import {
  isAbsolutePath,
  pathIsUnderProjectRoots,
} from "./reveal-in-file-manager.ts";
import { isTauriRuntime, tauriPlatform, type TauriPlatform } from "../runtime.ts";
import type { PathKind } from "./path-kind.ts";

export type DroppedItem =
  | { source: "path"; absolutePath: string }
  | { source: "file"; file: File };

export type DropEvent = {
  items: DroppedItem[];
  position?: { x: number; y: number };
};

export type DropClassification =
  | { kind: "path-file"; projectId: string; rootId: string; path: string }
  | { kind: "path-folder"; projectId: string; rootId: string; path: string }
  /** An image or video under a root uploads its bytes, so the model sees its pixels. */
  | { kind: "bytes-media"; absolutePath: string; rootId: string }
  | { kind: "external-file"; absolutePath: string }
  | { kind: "web-file"; file: File }
  | { kind: "reject"; reason: string };

export const DROP_REJECT_UNIMPORTABLE_PATH =
  "Drop an individual file to attach it. Folders must be inside a project folder.";

export const DROP_REJECT_MISSING = "That file no longer exists.";

export type RegisterChatDropHandlers = {
  onDragActive: (active: boolean) => void;
  onDrop: (e: DropEvent) => void;
  /** HTML5 drop surface; ignored by the desktop host. */
  htmlTarget?: HTMLElement;
};

/** Claims keep outgoing chat cleanup from releasing the incoming chat's handlers. */
const nativeDropClaim = createClaimable<RegisterChatDropHandlers>();

/** Shared by chat mounts for the lifetime of the window. */
let nativeDropSubscription: Promise<void> | undefined;

/** One window subscription avoids IPC during chat navigation. */
function ensureNativeDropSubscription(): void {
  if (nativeDropSubscription) return;
  nativeDropSubscription = (async () => {
    const { getCurrentWebview } = await import("@tauri-apps/api/webview");
    await getCurrentWebview().onDragDropEvent((event) => {
      const handlers = nativeDropClaim.get();
      // Native drops carry paths on this device, which only a shared-device host can read.
      if (!handlers || !hostSharesDevice()) return;
      const payload = event.payload;
      switch (payload.type) {
        case "enter":
        case "over":
          handlers.onDragActive(true);
          break;
        case "leave":
          handlers.onDragActive(false);
          break;
        case "drop":
          handlers.onDragActive(false);
          handlers.onDrop({
            items: payload.paths.map((absolutePath) => ({
              source: "path" as const,
              absolutePath,
            })),
            position: { x: payload.position.x, y: payload.position.y },
          });
          break;
      }
    });
  })().catch(() => {
    // A later mount can retry a failed subscription.
    nativeDropSubscription = undefined;
  });
}

export function registerChatDrop(handlers: RegisterChatDropHandlers): () => void {
  if (isTauriRuntime()) {
    const claimToken = {};
    nativeDropClaim.claim(handlers, claimToken);
    ensureNativeDropSubscription();
    return () => nativeDropClaim.release(claimToken);
  }

  if (!handlers.htmlTarget) return () => {};
  let detachHtml: (() => void) | undefined = attachHtml5Drop(
    handlers.htmlTarget,
    handlers,
  );
  return () => {
    detachHtml?.();
    detachHtml = undefined;
  };
}

/** Clears subscription state between tests. */
export function resetChatDropSubscriptionForTests(): void {
  nativeDropSubscription = undefined;
}

function attachHtml5Drop(
  target: HTMLElement,
  handlers: RegisterChatDropHandlers,
): () => void {
  let depth = 0;

  const onEnter = (e: DragEvent) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth += 1;
    handlers.onDragActive(true);
  };
  const onOver = (e: DragEvent) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
  };
  const onLeave = (e: DragEvent) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = Math.max(0, depth - 1);
    if (depth === 0) handlers.onDragActive(false);
  };
  const onDrop = (e: DragEvent) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = 0;
    handlers.onDragActive(false);
    const list = e.dataTransfer?.files;
    if (!list || list.length === 0) return;
    const items: DroppedItem[] = Array.from(list).map((file) => ({
      source: "file" as const,
      file,
    }));
    handlers.onDrop({
      items,
      position: { x: e.clientX, y: e.clientY },
    });
  };

  target.addEventListener("dragenter", onEnter);
  target.addEventListener("dragover", onOver);
  target.addEventListener("dragleave", onLeave);
  target.addEventListener("drop", onDrop);
  return () => {
    target.removeEventListener("dragenter", onEnter);
    target.removeEventListener("dragover", onOver);
    target.removeEventListener("dragleave", onLeave);
    target.removeEventListener("drop", onDrop);
  };
}

function hasFiles(e: DragEvent): boolean {
  const types = e.dataTransfer?.types;
  if (!types) return false;
  return Array.from(types).includes("Files");
}

/** Host path kinds separate project references from imported external files. */
export function classifyDroppedItem(
  item: DroppedItem,
  projectId: string,
  roots: readonly ResolveProjectRoot[],
  pathKindResult: PathKind | null,
  platform: TauriPlatform | null = tauriPlatform(),
): DropClassification {
  if (item.source === "file") {
    return { kind: "web-file", file: item.file };
  }

  const abs = item.absolutePath.trim();
  if (!abs || !isAbsolutePath(abs, platform)) {
    return { kind: "reject", reason: DROP_REJECT_UNIMPORTABLE_PATH };
  }

  const rootPaths = roots.map((r) => r.path).filter((p) => p.trim());
  const inProject = pathIsUnderProjectRoots(abs, rootPaths, platform);
  if (!inProject) {
    if (pathKindResult === "file") {
      return { kind: "external-file", absolutePath: abs };
    }
    if (pathKindResult === "missing") {
      return { kind: "reject", reason: DROP_REJECT_MISSING };
    }
    return { kind: "reject", reason: DROP_REJECT_UNIMPORTABLE_PATH };
  }

  const containingRoot = longestMatchingRoot(abs, roots, platform);
  if (!containingRoot) {
    return { kind: "reject", reason: DROP_REJECT_UNIMPORTABLE_PATH };
  }

  const kind = pathKindResult ?? "missing";
  if (kind === "missing") {
    return { kind: "reject", reason: DROP_REJECT_MISSING };
  }
  if (kind === "folder") {
    const rel = relativeUnderRoot(abs, containingRoot.path, platform);
    if (rel == null) {
      return { kind: "reject", reason: DROP_REJECT_UNIMPORTABLE_PATH };
    }
    return {
      kind: "path-folder",
      projectId,
      rootId: containingRoot.id,
      path: rel,
    };
  }

  if (mediaMimeFromPath(abs)) {
    return {
      kind: "bytes-media",
      absolutePath: abs,
      rootId: containingRoot.id,
    };
  }

  const rel = relativeUnderRoot(abs, containingRoot.path, platform);
  if (rel == null) {
    return { kind: "reject", reason: DROP_REJECT_UNIMPORTABLE_PATH };
  }
  return {
    kind: "path-file",
    projectId,
    rootId: containingRoot.id,
    path: rel,
  };
}
