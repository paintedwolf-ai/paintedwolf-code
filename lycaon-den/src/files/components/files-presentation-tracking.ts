import { createEffect, onCleanup } from "solid-js";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { isPresented } from "../../ui/presented.ts";
import type { FileBufferKey } from "./project-files-model.ts";
import type { FilesScope } from "./files-scope.ts";
import { scopeFileFor } from "../tree/scope-resolution.ts";
import {
  completeFilePresentation,
  filePresentationTarget,
  trackFilePresentation,
  type PresentationTarget,
} from "./presentation-tracking.ts";

type FilesPresentationOptions = Pick<FilesScope, "projectId" | "client" | "activeBuffer" |
  "editorPresentationPending" | "versionForBuffer" | "refreshScopeMarks"> & {
  presentationToken: string;
  stageEl: () => HTMLDivElement | undefined;
  stageReady: () => boolean;
  scopeContentVersion: () => number;
};

export function createFilesPresentationTracking(options: FilesPresentationOptions) {
  const { projectId: currentProjectId, client, presentationToken, activeBuffer, stageEl, stageReady,
    editorPresentationPending, scopeContentVersion, versionForBuffer, refreshScopeMarks } = options;
  const presentationInteractive = useResidentInteractive();
  const completedPresentations = new Map<string, number>();
  const completePresentation = (target: PresentationTarget, c = client(), projectId = currentProjectId()) => {
    if (!c) return;
    void completeFilePresentation(c, projectId, target)
      .then(() => {
        const key = `${projectId}\0${target.fileId}`;
        completedPresentations.set(key, Math.max(completedPresentations.get(key) ?? 0, target.ordinal));
        if (currentProjectId() !== projectId) return;
        void refreshScopeMarks();
      })
      .catch((error: unknown) => {
        if ((completedPresentations.get(`${projectId}\0${target.fileId}`) ?? 0) >= target.ordinal) return;
        reportSurfaceFailure(
          {
            code: "files_review_save_failed",
            title: "Could not confirm your review was saved",
            suggestedAction: "Reopen the file to confirm the review again.",
          },
          error instanceof Error && error.message ? error : "The host did not confirm the review.",
          projectId,
        );
      });
  };
  let presentedBufferKey: FileBufferKey | null = null;
  const endPresentation = () => {
    trackFilePresentation(currentProjectId(), presentationToken, null, completePresentation);
    presentedBufferKey = null;
  };
  createEffect(() => {
    void scopeContentVersion();
    const buf = activeBuffer();
    const stage = stageEl();
    const presentationClient = client();
    const presentationProject = currentProjectId();
    const finish = (target: PresentationTarget) => completePresentation(target, presentationClient, presentationProject);
    if (!buf || !presentationClient || !stage || !presentationInteractive() ||
      editorPresentationPending() || !stageReady() || buf.jobId || versionForBuffer(buf) ||
      buf.kind !== "text" || buf.loadError || buf.loading) {
      endPresentation();
      return;
    }
    if (presentedBufferKey !== buf.key) endPresentation();
    const file = scopeFileFor(currentProjectId(), buf.rootId, buf.path);
    const target = buf.dirty || buf.diverged || buf.editorResolving ? null
      : filePresentationTarget(file, buf.fileId, buf.baseSha256);
    const pane = stage.querySelector(".den-files-editor-pane") ?? stage;
    let disposed = false;
    const publish = (painted: boolean) => {
      if (disposed) return;
      if (!painted || document.visibilityState === "hidden" || !isPresented(pane)) {
        endPresentation();
      } else if (target) {
        trackFilePresentation(presentationProject, presentationToken, target, finish);
        presentedBufferKey = buf.key;
      }
    };
    let intersecting = false;
    const visibilityChanged = () => publish(intersecting);
    document.addEventListener("visibilitychange", visibilityChanged);
    const io = typeof IntersectionObserver !== "undefined" ? new IntersectionObserver((entries) => {
      intersecting = entries.some((entry) => entry.isIntersecting && entry.intersectionRatio > 0);
      publish(intersecting);
    }, { threshold: [0, 0.25, 1] }) : null;
    io?.observe(pane);
    if (!io) { intersecting = true; publish(true); }
    onCleanup(() => {
      disposed = true;
      io?.disconnect();
      document.removeEventListener("visibilitychange", visibilityChanged);
    });
  });
  onCleanup(endPresentation);

  return { completePresentation };
}
