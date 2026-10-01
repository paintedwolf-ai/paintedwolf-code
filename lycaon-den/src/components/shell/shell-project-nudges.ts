import { createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import { isChatActivityLive } from "../../chat/session/session-activity.ts";
import { captureProjectThumbnail } from "../../home/capture-thumbnail.ts";
import { draftPromoteFirstTurnComplete, rememberDraftProjectExchange, shouldShowDraftPromoteBanner } from "../../home/draft-promote.ts";
import { shouldShowNoFolderBanner } from "../../home/nofolder-banner.ts";
import { shouldCaptureProjectThumbnail } from "../../home/thumbnail-store.ts";
import { navHidePreviewed } from "../../shell/layout-store.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import type { ShellScope } from "./shell-scope.ts";

type ProjectNudgeDependencies = Pick<ShellScope,
  "appStore" | "activeProject" | "activeProjectId" | "activeChat" | "readyChat" | "stageScope" | "nav"> & {
  identityReady: () => boolean;
  windowNavCollapsed: () => boolean;
};

export function createShellProjectNudges({ appStore, activeProject, activeProjectId,
  activeChat, readyChat, stageScope, identityReady, windowNavCollapsed, nav }: ProjectNudgeDependencies) {
  // Folder-banner dismissal persists per project.
  const [draftPromoteDismissed, setDraftPromoteDismissed] = createSignal<
    ReadonlySet<string>
  >(new Set(getAppStateSnapshot().draftPromoteDismissed ?? []));
  const [nofolderDismissed, setNofolderDismissed] = createSignal<
    ReadonlySet<string>
  >(new Set(getAppStateSnapshot().nofolderDismissed ?? []));
  // Sticky across reloads — seeded from the persisted app-state snapshot.

  const [draftProjectsWithExchange, setDraftProjectsWithExchange] = createSignal<
    ReadonlySet<string>
  >(new Set());
  const dismissDraftPromoteBanner = (projectId: string) => {
    setDraftPromoteDismissed((prev) => {
      const next = new Set(prev).add(projectId);
      void persistAppStateInBackground({ draftPromoteDismissed: [...next] });
      return next;
    });
  };

  const dismissNoFolderBanner = (projectId: string) => {
    setNofolderDismissed((prev) => {
      const next = new Set(prev).add(projectId);
      void persistAppStateInBackground({ nofolderDismissed: [...next] });
      return next;
    });
  };

  const observePresentation = () => {
    const activeSessionHasExchange = createMemo(() => {
      const sessionId = appStore.state.currentSession?.id;
      return draftPromoteFirstTurnComplete({
        messages: appStore.state.messages,
        live: sessionId
          ? isChatActivityLive(appStore, sessionId)
          : false,
      });
    });

    createEffect(() => {
      const project = activeProject();
      if (!project?.is_draft || !activeSessionHasExchange()) return;
      setDraftProjectsWithExchange((prev) =>
        rememberDraftProjectExchange(prev, project.id),
      );
    });

    const showPromoteNudge = createMemo(() => {
      const project = activeProject();
      if (!project || !activeChat()) return false;
      return shouldShowDraftPromoteBanner({
        isDraft: project.is_draft,
        projectHasExchange: draftProjectsWithExchange().has(project.id),
        dismissed: draftPromoteDismissed().has(project.id),
        promotePending: project.promotion != null && project.promotion.phase !== "committed",
        saveActionVisible: identityReady() && !windowNavCollapsed() && !navHidePreviewed(),
      });
    });

    const showNoFolderBanner = createMemo(() => {
      const project = activeProject();
      const scope = stageScope();
      if (!project || scope.phase !== "ready" || !readyChat()) return false;
      return shouldShowNoFolderBanner({
        isDraft: !!project.is_draft,
        rootCount: project.roots?.length ?? 0,
        dismissed: nofolderDismissed().has(project.id),
        chatReady: true,
      });
    });

    // Project-card capture waits for chat activity to stop.
    createEffect(() => {
      const chat = activeChat();
      const projectId = activeProjectId();
      const messageCount = appStore.state.messages.length;
      const idle = !chat || !isChatActivityLive(appStore, chat.sessionId);
      if (nav() !== "projects" || !chat || !projectId || messageCount === 0 || !idle) {
        return;
      }
      let idleHandle: number | undefined;
      const capture = () => {
        // An untracked read prevents thumbnail capture from triggering itself.
        if (!shouldCaptureProjectThumbnail(projectId)) return;
        captureProjectThumbnail(projectId, {
          projectName: activeProject()?.name?.trim() || "Untitled project",
          sessionTitle:
            appStore.state.currentSession?.title?.trim() || "New session",
          messageCount,
          stage: nav(),
        });
      };
      // Capture work waits for an idle main thread.
      const captureWhenIdle = () => {
        const requestIdle =
          typeof window.requestIdleCallback === "function"
            ? window.requestIdleCallback.bind(window)
            : null;
        if (!requestIdle) {
          capture();
          return;
        }
        idleHandle = requestIdle(
          () => {
            idleHandle = undefined;
            capture();
          },
          { timeout: 4000 },
        );
      };
      const timer = setTimeout(captureWhenIdle, 1500);
      onCleanup(() => {
        clearTimeout(timer);
        if (idleHandle !== undefined && typeof cancelIdleCallback === "function") {
          cancelIdleCallback(idleHandle);
        }
      });
    });

    return { showPromoteNudge, showNoFolderBanner };
  };
  return { dismissDraftPromoteBanner, dismissNoFolderBanner, observePresentation };
}
