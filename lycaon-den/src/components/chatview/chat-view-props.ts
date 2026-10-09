import type { CoordinatorVisionSupport } from "../../chat/composer/composer-vision.ts";
import type { ProviderKindTemplate, ProviderMeta } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import { type OpenWorkerOptions, type WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

export type ChatViewProps = {
  appStore: AppStore;
  recents: RecentsStore;
  projects: ProjectsStore;
  projectId: string;
  projectDir: string;
  sessionId: string;
  hasInitialPrompt: boolean;
  /** False while this chat is a hidden resident surface. */
  surfaceActive?: boolean;
  selectedWorkerId?: string | null;
  workersDrawerOpen?: boolean;
  workerDrawerFocus?: WorkerDrawerFocus;
  workersBackgroundHydrate?: boolean;
  onWorkersClose?: () => void;
  onWorkerDrawerFocusHandled?: () => void;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  onOpenFiles: () => void;
  onSend: (
    payload: import("./Composer.tsx").ComposerSendPayload,
  ) => boolean | void | Promise<boolean | void>;
  onStop: () => void | Promise<void>;
  visionSupport?: CoordinatorVisionSupport;
  needsProvider?: boolean;
  providers?: readonly ProviderMeta[];
  providerKinds?: readonly ProviderKindTemplate[];
};
