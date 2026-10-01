import type { SourceViewsClient } from "../../api/source-views-client.ts";
import type { SourceTreeReviewScope } from "../../api/types.ts";
import { createContext, useContext } from "solid-js";
import type { ProjectRoot, SourceChange } from "../../api/types.ts";
import { type ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import type { FilesContextTarget } from "../commands/project-files-context-menu.ts";
import { type NewEntryKind } from "../commands/project-files-create.ts";
import { type FilesAgentPresence } from "../components/files-agent-presence.ts";
import { type FlatTreeRow } from "./project-files-tree-flat.ts";
import { type StickyFold } from "./files-tree-sticky.ts";

export type FilesTreeSelection = {
  rootId: string;
  rootLabel: string;
  path: string;
};

export type FilesTreeEntrySelection = {
  rootId: string;
  path: string;
  kind: "file" | "folder";
};

export type FilesTreeFolderScope = {
  rootId: string;
  /** `"."` scopes the whole root. */
  dir: string;
};

export type ActiveFile = { rootId: string; path: string; revision?: number } | null;

/** Creates one entry under a root; resolves with the created root-relative path. */
export type CreateEntry = (
  kind: NewEntryKind,
  rootId: string,
  path: string,
) => Promise<string>;

/** Commands the stage sends to its mounted tree. */
export type FilesTreeHandle = {
  startCreate: (kind: NewEntryKind, rootId: string, dir: string) => void;
  /** Applies one confirmed host mutation to the shared tree. */
  confirmChange: (change: SourceChange) => void;
  revealPath: (rootId: string, path: string, isDir: boolean) => void;
  collapseAll: (scope?: FilesTreeFolderScope) => void;
  expandAll: (scope?: FilesTreeFolderScope) => void;
};

export type FilesTreeProps = {
  projectId: string;
  /** Physical checkout identity returned by the host. */
  workspaceId: string;
  /** Whether the Files pane itself is exposed inside its resident stage. */
  visible?: boolean;
  roots: ProjectRoot[];
  client: SourceViewsClient | null;
  sessionId?: string;
  reviewScope?: SourceTreeReviewScope;
  activeFile: ActiveFile;
  navigationRevision?: number;
  navigationOrigin?: "reader" | "presentation" | null;
  autoReveal?: boolean;
  retainPresentation?: boolean;
  onRevealMotionStart?: () => void;
  selectedEntry: FilesTreeEntrySelection | null;
  onSelectEntry: (selection: FilesTreeEntrySelection) => void;
  /** Agent presence for a file row, projected from structured tool calls. */
  agentPresenceForPath?: (rootId: string, path: string) => FilesAgentPresence | null;
  /** Path is in the sidebar's current review scope. */
  inScope?: (rootId: string, path: string) => boolean;
  /** Current comparison created this path. */
  addedInScope?: (rootId: string, path: string) => boolean;
  onOpenFile: (
    selection: FilesTreeSelection,
    intent: "transient" | "permanent",
  ) => void;
  onRevertFile?: (rootId: string, path: string) => void;
  createEntry: CreateEntry;
  filterQuery?: string;
  onFilterQueryChange?: (q: string) => void;
  filterOpen?: boolean;
  onFilterClose?: () => void;
  renaming?: { rootId: string; path: string } | null;
  onCommitRename?: (
    rootId: string,
    from: string,
    to: string,
    isDir: boolean,
  ) => Promise<void>;
  onCancelRename?: () => void;
  onMovePath?: (
    rootId: string,
    from: string,
    toDir: string,
    isDir: boolean,
  ) => Promise<void>;
  onRowMenu?: (anchor: ContextMenuAnchor, target: FilesContextTarget) => void;
  /** Receives the tree's commands once it mounts. */
  onReady?: (tree: FilesTreeHandle) => void;
  /** Receives the workspace identified by a conflicting response. */
  onWorkspaceMismatch?: (workspaceId: string) => void;
  /** Signals when initial root listings settle. */
  onInitialLoadSettled?: (workspaceId: string) => void;
};

export type DirState = {
  expanded: boolean;
  naming: NewEntryKind | null;
  namingDraft: string;
  createError: string | null;
  creating: boolean;
};

export type DragSource = {
  rootId: string;
  path: string;
  name: string;
  isDir: boolean;
};
export type DropTarget = { rootId: string; dir: string };
export type DropAssessment = {
  valid: boolean;
  destination: string;
  reason?: string;
};
export type PendingMove = { source: DragSource; target: DropTarget };

export type TreeOps = {
  fold: () => StickyFold;
  props: FilesTreeProps;
  dirState: (rootId: string, dir: string) => DirState;
  toggleDir: (
    rootId: string,
    dir: string,
    opts?: { recursiveCollapse?: boolean },
  ) => Promise<void>;
  selectFolder: (
    rootId: string,
    dir: string,
    forceOpen?: boolean,
  ) => Promise<void>;
  openFile: (
    selection: FilesTreeSelection,
    intent: "transient" | "permanent",
  ) => void;
  startNaming: (
    rootId: string,
    dir: string,
    kind: NewEntryKind,
  ) => Promise<void>;
  cancelNaming: (rootId: string, dir: string) => void;
  setNamingDraft: (rootId: string, dir: string, draft: string) => void;
  submitName: (
    rootId: string,
    rootLabel: string,
    dir: string,
    typed: string,
  ) => Promise<void>;
  onRowPointerDown: (
    rootId: string,
    path: string,
    name: string,
    isDir: boolean,
    e: PointerEvent,
  ) => void;
  dragSource: () => DragSource | null;
  dropTarget: () => DropTarget | null;
  dropAssessment: () => DropAssessment | null;
  movePending: () => PendingMove | null;
  openRowMenu: (e: MouseEvent, target: FilesContextTarget) => void;
  isTreeTabStop: (row: FlatTreeRow) => boolean;
  rememberTreeFocus: (row: FlatTreeRow) => void;
  submitRename: (
    rootId: string,
    path: string,
    typed: string,
    isDir: boolean,
  ) => Promise<boolean>;
  renameError: () => string | null;
  renamingBusy: () => boolean;
  cancelRename: () => void;
  /** Reveals and focuses a row below the sticky stack. */
  activateStickyFolder: (
    displayIndex: number,
    rootId: string,
    dir: string,
  ) => void;
};

export const TreeContext = createContext<TreeOps>();

export function useTree(): TreeOps {
  const ctx = useContext(TreeContext);
  if (!ctx) throw new Error("FilesTree components must render under FilesTree");
  return ctx;
}
