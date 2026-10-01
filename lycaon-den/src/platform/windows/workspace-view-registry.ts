import { createSignal } from "solid-js";
import { invoke } from "@tauri-apps/api/core";
import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "./window-channel.ts";
import { windowViewId } from "./window-subject.ts";
import { createFramePublication } from "../../ui/frame-publication.ts";

/** A context on screen in one workspace view. */
export type WorkspaceContext =
  | {
      kind: "session";
      projectId: string;
      sessionId: string;
    }
  | {
      kind: "stage";
      projectId?: string;
      stageId: string;
    }
  | {
      kind: "panel";
      panelId: string;
      projectId?: string;
    };

type WorkspaceViewPresentation = {
  label: string;
  viewId: string;
  contexts: readonly WorkspaceContext[];
};

const currentViewId = windowViewId();
const [currentContexts, setCurrentContexts] = createSignal<
  readonly WorkspaceContext[]
>([]);
const [presentations, setPresentations] = createSignal<
  readonly WorkspaceViewPresentation[]
>([]);
let mirrorListening = false;
let lastPublished = "";
let lastRequested = "";
let publicationRevision = 0;
let pendingHostPublication: PendingWorkspacePublication | undefined;
let hostPublicationInFlight = false;

type PendingWorkspacePublication = {
  serialized: string;
  contexts: readonly WorkspaceContext[];
  revision: number;
};

async function publishPendingHostContexts(): Promise<void> {
  if (hostPublicationInFlight) return;
  hostPublicationInFlight = true;
  try {
    while (pendingHostPublication) {
      const next = pendingHostPublication;
      pendingHostPublication = undefined;
      if (next.serialized === lastPublished) continue;
      try {
        await invoke("set_workspace_view_contexts", {
          viewId: currentViewId,
          contexts: next.contexts,
        });
        if (next.revision === publicationRevision) {
          lastPublished = next.serialized;
        }
      } catch (err) {
        if (next.revision !== publicationRevision) continue;
        if (lastRequested === next.serialized) lastRequested = lastPublished;
        console.debug("[workspace-views] presentation publish failed", err);
      }
    }
  } finally {
    hostPublicationInFlight = false;
  }
}

const hostPublication = createFramePublication<PendingWorkspacePublication>(
  (next) => {
    pendingHostPublication = next;
    void publishPendingHostContexts();
  },
);

export function workspaceContextEqual(
  left: WorkspaceContext,
  right: WorkspaceContext,
): boolean {
  if (left.kind !== right.kind) return false;
  switch (left.kind) {
    case "session":
      return (
        right.kind === "session" &&
        left.projectId === right.projectId &&
        left.sessionId === right.sessionId
      );
    case "stage":
      return (
        right.kind === "stage" &&
        (left.projectId ?? "") === (right.projectId ?? "") &&
        left.stageId === right.stageId
      );
    case "panel":
      return (
        right.kind === "panel" &&
        left.panelId === right.panelId &&
        (left.projectId ?? "") === (right.projectId ?? "")
      );
  }
}

/** True when this is a peer view and the main window presents an exact context. */
export function mainWorkspaceViewPresents(context: WorkspaceContext): boolean {
  return (
    currentViewId !== "main" &&
    presentations().some(
      (view) =>
        view.label === "main" &&
        view.contexts.some((row) => workspaceContextEqual(row, context)),
    )
  );
}

/** True when an exact context is visible in any workspace view. */
export function workspaceContextIsOpen(context: WorkspaceContext): boolean {
  if (currentContexts().some((row) => workspaceContextEqual(row, context))) {
    return true;
  }
  return presentations().some(
    (view) =>
      view.viewId !== currentViewId &&
      view.contexts.some((row) => workspaceContextEqual(row, context)),
  );
}

export function setCurrentWorkspaceContexts(
  contexts: readonly WorkspaceContext[],
): void {
  const snapshot = [...contexts];
  setCurrentContexts(snapshot);
  const serialized = JSON.stringify(snapshot);

  if (!isTauriRuntime()) {
    if (serialized === lastPublished) return;
    lastPublished = serialized;
    lastRequested = serialized;
    setPresentations([{ label: "main", viewId: currentViewId, contexts: snapshot }]);
    return;
  }
  if (serialized === lastRequested) return;
  lastRequested = serialized;
  hostPublication.schedule({
    serialized,
    contexts: snapshot,
    revision: publicationRevision,
  });
}

async function refreshWorkspaceViewPresentations(): Promise<void> {
  if (!isTauriRuntime()) return;
  setPresentations(
    await invoke<WorkspaceViewPresentation[]>("list_workspace_view_presentations"),
  );
}

/** Mirror every live window presentation from the host-managed registry. */
export async function startWorkspaceViewPresentationMirror(): Promise<
  () => void
> {
  if (!isTauriRuntime()) return () => {};
  if (mirrorListening) {
    await refreshWorkspaceViewPresentations();
    return () => {};
  }
  mirrorListening = true;
  let unlisten: (() => void) | undefined;
  try {
    unlisten = await listenHostEvent<WorkspaceViewPresentation[]>(
      "workspace-view-presentations-changed",
      (event) => setPresentations(event.payload),
    );
    await refreshWorkspaceViewPresentations();
    return () => {
      mirrorListening = false;
      unlisten?.();
    };
  } catch (err) {
    mirrorListening = false;
    unlisten?.();
    throw err;
  }
}

/** Test-only registry reset. */
export function resetWorkspaceViewRegistryForTests(): void {
  publicationRevision += 1;
  hostPublication.cancel();
  pendingHostPublication = undefined;
  setCurrentContexts([]);
  setPresentations([]);
  mirrorListening = false;
  lastPublished = "";
  lastRequested = "";
}

/** Test-only host-mirror injection. */
export function setWorkspaceViewPresentationsForTests(
  next: readonly WorkspaceViewPresentation[],
): void {
  setPresentations(next);
}
