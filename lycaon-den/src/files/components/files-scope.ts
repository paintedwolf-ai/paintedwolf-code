import type { EditorView } from "@codemirror/view";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot } from "../../api/types.ts";
import type { LifecycleHooks } from "../commands/project-files-lifecycle.ts";
import type { FilesBufferRelease } from "../documents/project-files-buffers.ts";
import type { FileBuffer, ProjectFilesState } from "../documents/files-buffer-state.ts";
import type { FileVersionView } from "../history/file-version.ts";
import type { FileBufferKey } from "./project-files-model.ts";

/** What one Files stage shares with the controllers it creates; each declares its `Pick`. */
export type FilesScope = {
  projectId: () => string;
  client: () => LycaonClient | null;
  /** Reads the request’s chat address without creating a reactive dependency. */
  sourceSessionId: () => string | undefined;
  /** The workspace this stage displays; state that outlives one checkout is keyed by it. */
  filesWorkspaceId: () => string;
  live: () => boolean;
  state: () => ProjectFilesState;
  activeBuffer: () => FileBuffer | null;
  /** The tab the strip follows, which can lead the presented `activeKey`. */
  aimedKey: () => FileBufferKey | null;
  editorPresentationPending: () => boolean;
  filesRoots: () => ProjectRoot[];
  rootLabelFor: (rootId: string) => string;
  stripOrder: () => FileBufferKey[];
  tabsElement: () => HTMLDivElement | undefined;
  scrollerElement: () => HTMLDivElement | undefined;
  setSaveError: (message: string | null) => void;
  versionForBuffer: (buffer: FileBuffer) => FileVersionView | null;
  dropFileVersionState: (key: FileBufferKey) => void;
  loadVersionHistory: (buffer: FileBuffer) => void;
  refreshScopeMarks: () => Promise<void>;
  lifecycleHooks: () => LifecycleHooks;
  reloadBuffer: (key: FileBufferKey) => void;
  closeBuffer: (key: FileBufferKey, options?: { discardDraft?: boolean }) => Promise<FilesBufferRelease> | null;
  copyAbsolutePathRef: (rootId: string, path: string) => void;
  copyRelativePathRef: (rootId: string, path: string) => void;
};

/** What one FilesEditor shares with the controllers it creates. */
export type FilesEditorScope = {
  projectId: () => string;
  sessionId: () => string | undefined;
  client: () => LycaonClient | null | undefined;
  buffer: () => FileBuffer;
  live: () => boolean;
  /** Gutter and annotation extensions are installed on the view. */
  chromeReady: () => boolean;
  /** A past version replaces the live document. */
  historicalActive: () => boolean;
  /** The bound view, read without tracking. */
  currentView: () => EditorView | undefined;
  editorView: () => EditorView | undefined;
};
