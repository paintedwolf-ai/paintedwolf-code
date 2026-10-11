import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot } from "../../api/types.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { type FileTabWindowDrag, type ItemWindowDropPosition } from "../../platform/windows/item-windows.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { type FileBufferKey } from "./project-files-model.ts";

export type ProjectFilesViewProps = {
  projectId: string;
  projectName?: string;
  appStore: AppStore;
  roots: ProjectRoot[];
  client?: LycaonClient | null;
  /** The shell handles session navigation for gutter jumps. */
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
  onOpenSearch?: (args: {
    seed: string;
    originProjectId: string | null;
  }) => void;
  /** Tab-strip pull-off into an independent peer view. */
  onOpenInNewWindow?: (
    bufferKey?: FileBufferKey,
    position?: ItemWindowDropPosition,
  ) => void | Promise<void>;
  /** Tree-file pop-out into an independent peer view. */
  onOpenFileInNewWindow?: (
    rootId: string,
    path: string,
    title: string,
  ) => void | Promise<void>;
  /** Native destination window used while a tab is being torn off the strip. */
  fileTabWindowDrag?: FileTabWindowDrag;
  /** Called after a tear-off has successfully removed its one source tab. */
  onFileTabMovedOut?: (bufferKey: FileBufferKey) => void | Promise<void>;
  /** A file-addressed peer starts with only its requested tab. */
  restoreHotExit?: boolean;
  /** The window handles close-tab shortcuts when no tab is active. */
  onCloseTabIdle?: () => void;
  /** Document windows omit project navigation. */
  document?: {
    rootId: string;
    path: string;
    initialMarkdownView?: "code" | "preview";
    previewSource?: (source: string) => string;
    onSessionChange?: (session: ProjectFileDocumentSession | null) => void;
  };
};

export type ProjectFileDocumentSession = {
  save: () => Promise<boolean>;
};

