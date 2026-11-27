import { createEffect, createRoot } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import { CLIENT_NOTICES } from "../notices/client-notices.generated.ts";
import { projectScope } from "../notices/notice-scope.ts";
import type { NoticeStore } from "../notices/notice-store.ts";
import { openRunningStage } from "../platform/navigation/open-running.ts";
import {
  allPublishedProjectWatches,
  ensureProjectWatchesHydrated,
} from "./watches-store.ts";

/** Publishes and clears project-scoped `standing_watch_paused` and `standing_watch_disabled` notices. */
export function mountStandingWatchNotices(options: {
  notices: NoticeStore;
  activeProjectId?: () => string | null | undefined;
  getClient?: () => LycaonClient | null;
}): () => void {
  return createRoot((dispose) => {
    const publishedPaused = new Map<string, string>();
    const publishedDisabled = new Map<string, string>();

    createEffect(() => {
      const pid = options.activeProjectId?.()?.trim();
      const client = options.getClient?.() ?? null;
      if (pid && client) {
        ensureProjectWatchesHydrated(client, pid);
      }
    });

    createEffect(() => {
      const views = allPublishedProjectWatches();
      const seenProjects = new Set<string>();

      for (const [projectId, view] of views.entries()) {
        seenProjects.add(projectId);
        const scope = projectScope(projectId);

        const pausedWatch = view.standingWatches.find(
          (w) =>
            w.state === "paused" &&
            (w.state_reason === "consecutive_failures" ||
              w.state_reason === "target_unavailable"),
        );
        const pausedIdentity = pausedWatch
          ? `${pausedWatch.watch_id}:${pausedWatch.state_reason ?? ""}:${pausedWatch.revision}`
          : undefined;
        if (publishedPaused.get(projectId) !== pausedIdentity) {
          if (!pausedWatch || !pausedIdentity) {
            publishedPaused.delete(projectId);
            options.notices.withdraw("standing_watch_paused", scope);
          } else {
            publishedPaused.set(projectId, pausedIdentity);
            const copy = CLIENT_NOTICES.standing_watch_paused;
            const targetId = pausedWatch.watch_id;
            options.notices.publish(
              {
                code: "standing_watch_paused",
                severity: "warning",
                title: copy.title,
                message: `${pausedWatch.name} — ${copy.message}`,
                suggestedAction: copy.suggestedAction,
                interaction: {
                  actions: [
                    {
                      id: "open-activity",
                      label: "Open in Activity",
                      variant: "secondary",
                      run: () =>
                        openRunningStage({ projectId, watchId: targetId }),
                    },
                  ],
                },
              },
              scope,
            );
          }
        }

        const disabledWatch = view.standingWatches.find(
          (w) => w.state === "disabled",
        );
        const disabledIdentity = disabledWatch
          ? `${disabledWatch.watch_id}:${disabledWatch.revision}`
          : undefined;
        if (publishedDisabled.get(projectId) !== disabledIdentity) {
          if (!disabledWatch || !disabledIdentity) {
            publishedDisabled.delete(projectId);
            options.notices.withdraw("standing_watch_disabled", scope);
          } else {
            publishedDisabled.set(projectId, disabledIdentity);
            const copy = CLIENT_NOTICES.standing_watch_disabled;
            const targetId = disabledWatch.watch_id;
            options.notices.publish(
              {
                code: "standing_watch_disabled",
                severity: "warning",
                title: copy.title,
                message: `${disabledWatch.name} — ${copy.message}`,
                suggestedAction: copy.suggestedAction,
                interaction: {
                  actions: [
                    {
                      id: "open-activity",
                      label: "Open in Activity",
                      variant: "secondary",
                      run: () =>
                        openRunningStage({ projectId, watchId: targetId }),
                    },
                  ],
                },
              },
              scope,
            );
          }
        }
      }

      for (const projectId of [...publishedPaused.keys()]) {
        if (!seenProjects.has(projectId)) {
          publishedPaused.delete(projectId);
          options.notices.withdraw(
            "standing_watch_paused",
            projectScope(projectId),
          );
        }
      }
      for (const projectId of [...publishedDisabled.keys()]) {
        if (!seenProjects.has(projectId)) {
          publishedDisabled.delete(projectId);
          options.notices.withdraw(
            "standing_watch_disabled",
            projectScope(projectId),
          );
        }
      }
    });

    return dispose;
  });
}
