import { openSourceLocation } from "../platform/navigation/open-source.ts";
import type { ContextActionHandler } from "./context-actions.ts";
import type { ContextMenuItem } from "./ContextMenu.tsx";
import { pathMenuItems } from "./path-menu-items.ts";
import { copyTextToClipboard } from "../utils/clipboard.ts";
import {
  basenameOfPath,
  longestMatchingRoot,
  relativeUnderRoot,
  type ResolveProjectRoot,
} from "../api/project-path.ts";
import {
  addToChat,
  type ChatAttachmentRef,
} from "../chat/composer/add-to-chat.ts";
import type { ChatDestination } from "../chat/composer/shared-composer-document.ts";

export type ProjectPathMenuEntryKind = "file" | "folder";

export type ProjectPathMenuItemsOptions = {
  absolutePath: string;
  projectId: string;
  rootId?: string;
  jobId?: string;
  entryKind: ProjectPathMenuEntryKind;
  line?: number;
  rootRefs: readonly ResolveProjectRoot[];
  /** Null omits Add to chat; absent opens the destination chooser. */
  chatDestination?: ChatDestination | null;
  onOpen?: ContextActionHandler;
  onRevealInTree?: ContextActionHandler;
};

export function projectPathChatRef(
  opts: ProjectPathMenuItemsOptions,
): ChatAttachmentRef | null {
  if (opts.chatDestination === null || opts.jobId) return null;
  const abs = opts.absolutePath.trim();
  const projectId = opts.projectId.trim();
  const refs = opts.rootId ? opts.rootRefs.filter((root) => root.id === opts.rootId) : opts.rootRefs;
  if (!projectId || refs.length === 0 || !abs) return null;
  const root = longestMatchingRoot(abs, refs);
  const path = root ? relativeUnderRoot(abs, root.path) : null;
  if (!root || path == null) return null;
  const name = basenameOfPath(path === "." ? root.path : path);
  return opts.entryKind === "folder"
    ? { kind: "path-folder", projectId, rootId: root.id, path, name }
    : { kind: "path-file", projectId, rootId: root.id, path, name };
}

export function projectPathMenuItems(
  opts: ProjectPathMenuItemsOptions,
): ContextMenuItem[] {
  const abs = opts.absolutePath.trim();
  const rootPaths = opts.rootRefs.map((root) => root.path);
  const unavailable = {
    disabled: true as const,
    description: "This action requires the worker workspace location.",
  };

  const roots = opts.rootId ? opts.rootRefs.filter((root) => root.id === opts.rootId) : opts.rootRefs;
  const root = abs ? longestMatchingRoot(abs, roots) : undefined;
  const candidate = root ? relativeUnderRoot(abs, root.path) : null;
  const rel = candidate === "." ? null : candidate;

  const chatRef = projectPathChatRef(opts);
  return pathMenuItems({
    open: opts.onOpen ? { testId: "path-menu-open", onSelect: opts.onOpen } : undefined,
    revealInTree: root && candidate != null && opts.projectId.trim() && !opts.jobId ? {
      testId: "path-menu-reveal-tree",
      onSelect: opts.onRevealInTree ?? (() => {
        return openSourceLocation({ action: "reveal", intent: "permanent", projectId: opts.projectId, rootId: root.id, path: candidate, entryKind: opts.entryKind });
      }),
    } : undefined,
    openIn: { absolutePath: abs, projectRoots: rootPaths, entryKind: opts.entryKind, line: opts.line,
      unavailable: opts.jobId ? unavailable.description : undefined },
    copyAbsolute: abs ? opts.jobId ? unavailable : () => copyTextToClipboard(abs) : null,
    copyRelative: rel ? () => copyTextToClipboard(rel) : null,
    absoluteTestId: "path-menu-copy-path",
    relativeTestId: "path-menu-copy-relative-path",
    addToChat: chatRef ? {
      testId: "menu-add-to-chat",
      onSelect: () => {
        if (opts.chatDestination) {
          return addToChat(chatRef, { destination: opts.chatDestination });
        } else {
          return addToChat(chatRef);
        }
      },
    } : undefined,
  });
}
