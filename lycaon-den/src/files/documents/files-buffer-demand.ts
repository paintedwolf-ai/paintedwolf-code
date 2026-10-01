import { createEffect, createMemo, onCleanup, untrack } from "solid-js";
import { filesBufferNeedsBody, type FileBuffer } from "./files-buffer-state.ts";
import { markFilesBufferLoading, presentFilesBufferVersion } from "./project-files-buffers.ts";
import { scheduleFilesHotExitPersist } from "./files-hot-exit.ts";
import { bufferRequestIdentity, sharedBufferLoadRequests } from "./files-buffer-request-tracking.ts";
import { parseFilesPaneKey } from "../components/files-pane-key.ts";
import {
  filesPresentationLivenessViolation,
  reportFilesPresentationLivenessViolation,
} from "../components/files-presentation-liveness.ts";
import type { FilesScope } from "../components/files-scope.ts";

type BufferDemandOptions = Pick<FilesScope, "projectId" | "state" | "live" | "aimedKey" | "versionForBuffer" |
  "editorPresentationPending"> & {
  workspaceSettled: () => boolean;
  loadBuffer: (buffer: FileBuffer) => void;
  displayedPane: () => string | null;
  pendingPreparation: (buffer: FileBuffer | undefined) => string[];
};

export function observeFilesBufferDemand(options: BufferDemandOptions) {
  const { projectId, state, live, aimedKey, versionForBuffer, workspaceSettled, loadBuffer,
    displayedPane, editorPresentationPending, pendingPreparation } = options;
  let hotExitPresentationKey = "";
  let hotExitActiveKey = "";
  createEffect(() => {
    const s = state();
    const aimed = aimedKey();
    const active = aimed ? s.byKey[aimed] : undefined;
    if (active && !active.preview && !active.jobId) {
      hotExitActiveKey = active.key;
    }
    const durable: string[] = [hotExitActiveKey];
    for (const key of s.order) {
      const buf = s.byKey[key];
      if (!buf || buf.preview || buf.jobId) continue;
      durable.push([
        buf.rootId,
        buf.rootLabel,
        buf.path,
        buf.kind,
        buf.encoding ?? "",
        buf.pinned ? "pinned" : "",
      ].join("\0"));
    }
    const next = durable.join("\x01");
    if (next === hotExitPresentationKey) return;
    hotExitPresentationKey = next;
    scheduleFilesHotExitPersist(projectId());
  });

  // File loading retains the displayed editor.
  createEffect(() => {
    if (!live()) return;
    const key = aimedKey();
    const buf = key ? state().byKey[key] : undefined;
    if (!buf) return;
    if (buf.content.state === "suspended" && !buf.loading && !buf.loadError && !versionForBuffer(buf)) {
      untrack(() => markFilesBufferLoading(projectId(), buf.key));
      return;
    }
    if (!buf.loading) return;
    if (versionForBuffer(buf)) {
      // A past version is its own content; the working file is read on return.
      presentFilesBufferVersion(projectId(), buf.key);
      return;
    }
    if (!buf.jobId && !workspaceSettled()) return;
    loadBuffer(buf);
  });

  // A selection warms each neighbour once; eviction does not renew speculative demand.
  let prefetchedAim: string | null = null;
  const prefetchedNeighbors = new Set<string>();
  createEffect(() => {
    if (!live()) return;
    const key = aimedKey();
    const s = state();
    if (prefetchedAim !== key) {
      prefetchedAim = key;
      prefetchedNeighbors.clear();
      const index = key ? s.order.indexOf(key) : -1;
      for (const neighbor of [s.order[index - 1], s.order[index + 1]]) {
        const buffer = neighbor ? s.byKey[neighbor] : undefined;
        if (buffer && !filesBufferNeedsBody(buffer)) prefetchedNeighbors.add(buffer.key);
      }
    }
    if (!key || s.byKey[key]?.loading !== false) return;
    const requested = s.byKey[key];
    if (requested?.editorOpening) return;
    const index = s.order.indexOf(key);
    for (const neighbor of [s.order[index - 1], s.order[index + 1]]) {
      const buf = neighbor ? s.byKey[neighbor] : undefined;
      if (!buf || prefetchedNeighbors.has(buf.key) || buf.loadError || versionForBuffer(buf)) continue;
      if (!buf.jobId && !workspaceSettled()) continue;
      prefetchedNeighbors.add(buf.key);
      if (buf.content.state === "suspended" && !buf.loading) untrack(() => markFilesBufferLoading(projectId(), buf.key));
      if (buf.loading) loadBuffer(buf);
    }
  });

  // An idle stage keeps its aim cold, so only a live stage owes a presentation.
  const presentationAim = createMemo(() => ({ key: aimedKey(), revision: state().aimRevision,
    pane: displayedPane(), pending: editorPresentationPending() && live() }), undefined,
    { equals: (a, b) => !!a && a.key === b.key && a.revision === b.revision && a.pane === b.pane && a.pending === b.pending });
  createEffect(() => {
    const { key, revision, pane, pending } = presentationAim();
    if (!key || !pending) return;
    const started = Date.now();
    const inspect = () => {
      if (state().aimRevision !== revision || aimedKey() !== key || !editorPresentationPending() || !live()) return;
      const buf = state().byKey[key];
      const violation = filesPresentationLivenessViolation({
        projectId: projectId(), requestedKey: key, presentationPending: editorPresentationPending(),
        displayedKey: pane ? parseFilesPaneKey(pane)?.key ?? null : null,
        readInFlight: !!buf && sharedBufferLoadRequests.inFlight(bufferRequestIdentity(projectId(), buf)),
        workspaceResolving: !buf?.jobId && !workspaceSettled(),
        preparing: pendingPreparation(buf),
        elapsedMs: Date.now() - started,
      });
      if (violation) reportFilesPresentationLivenessViolation(violation);
    };
    const paintCheck = setTimeout(inspect, 300);
    const delayedCheck = setTimeout(inspect, 10_000);
    onCleanup(() => { clearTimeout(paintCheck); clearTimeout(delayedCheck); });
  });

}
