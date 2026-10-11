import { type ShellScope } from "./shell-scope.ts";
import { createEffect, createMemo } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { type WindowSubject } from "../../platform/windows/window-subject.ts";
import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import { currentSplitFocusRegion } from "../../shell/layout-store.ts";
import { resolvePeerOpenSubject, type PeerOpenSubject, peerWindowsForSubject } from "../../shortcuts/peer-view-subject.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { stageLabelFor } from "../stage/stage-registry.tsx";
import { beginFileItemWindowDrag, closeItemWindow, focusItemWindow, itemWindowViews, openItemWindow, type ItemWindowDropPosition } from "../../platform/windows/item-windows.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { resolveSourceRequest } from "../../platform/navigation/open-source.ts";
import { currentWindowNumber, nextWindowLabel, windowLabelForNumber } from "../../platform/windows/window-numbers.ts";
import { mainWorkspaceViewPresents } from "../../platform/windows/workspace-view-registry.ts";
import { editorDocumentParticipantsFor } from "../../files/documents/editor-document.ts";
import { type FileBufferKey } from "../../files/components/project-files-model.ts";
import { openSourceInFilesStage } from "../../files/components/project-files-open.ts";

type PeerWindowDependencies = Pick<ShellScope,
  "shell" | "appStore" | "projects" | "activeProjectId" | "activeProject" | "stageAvailable" | "projectSessions" | "activeChat"> & {
  /** What this window presents when it is itself a peer window. */
  itemSubject: WindowSubject | null;
  stageColumnStageId: () => ContextNavItemId | null;
  placeableStageId: () => ContextNavItemId | null;
  splitLive: () => boolean;
  openStage: (stage: ContextNavItemId) => void;
};

export type ShellPeerWindows = ReturnType<typeof createShellPeerWindows>;

/** The peer windows beside this one: what they show, and opening, raising, and closing them.
 * A peer window presents its own subject once its project is known. */
export function createShellPeerWindows({ shell, appStore, projects, activeProjectId, activeProject,
  stageAvailable, itemSubject, stageColumnStageId, placeableStageId, splitLive, projectSessions, activeChat, openStage }: PeerWindowDependencies) {
  const activeFileBuffer = () => {
    const projectId = shell.state.activeProjectId;
    const filesState = projectId != null ? projectFilesState(projectId) : null;
    const activeKey = filesState?.activeKey ?? null;
    return activeKey && filesState ? filesState.byKey[activeKey] ?? null : null;
  };

  /** Title of the selected chat, which may not be the one bound mid-switch. */
  const selectedChatTitle = (sessionId: string): string => {
    const current = appStore.state.currentSession;
    const bound = current?.id === sessionId ? current.title?.trim() : undefined;
    const row = projectSessions.state.rows.find((r) => r.id === sessionId);
    return bound || row?.title?.trim() || sessionId;
  };

  const focusedPeerSubject = (): PeerOpenSubject | null => {
    const projectId = shell.state.activeProjectId;
    const chat = activeChat();
    const stageId = stageColumnStageId() ?? placeableStageId();
    const activeBuf = activeFileBuffer();
    return resolvePeerOpenSubject({
      windowSubject: itemSubject,
      projectId,
      session: chat
        ? { sessionId: chat.sessionId, title: selectedChatTitle(chat.sessionId) }
        : null,
      contextStage: stageId,
      contextTitle: stageId ? stageLabelFor(stageId) : null,
      activeFile:
        activeBuf && stageId === "files"
          ? {
              rootId: activeBuf.rootId,
              path: activeBuf.path,
              title: activeBuf.name,
            }
          : itemSubject?.kind === "file"
            ? {
                rootId: itemSubject.rootId,
                path: itemSubject.path,
                title: itemSubject.path.split("/").pop() || "File",
              }
            : null,
      filesStageWithoutEditor: stageId === "files" && activeBuf == null,
      splitFocusRegion: splitLive() ? currentSplitFocusRegion() : null,
      splitLive: splitLive(),
    });
  };

  let itemFileOpened = false;
  createEffect(() => {
    if (
      itemFileOpened ||
      itemSubject?.kind !== "file" ||
      appStore.state.sidecarStatus !== "connected"
    ) {
      return;
    }
    // Subject hydration retriggers navigation.
    const project = projects.byId(itemSubject.projectId);
    if (!project) return;
    // Files mounts after its project is bound.
    if (shell.state.activeProjectId !== itemSubject.projectId) {
      shell.beginStageSwitch({
        projectId: itemSubject.projectId,
        kind: "cross-project",
      });
    }
    itemFileOpened = true;
    void (async () => {
      const resolved = resolveSourceRequest(
        {
          projectId: itemSubject.projectId,
          rootId: itemSubject.rootId,
          path: itemSubject.path,
          intent: "permanent",
        },
        () => ({
          roots: project.roots.filter((root) => root.id === itemSubject.rootId),
        }),
      );
      if (resolved.status !== "resolved") {
        itemFileOpened = false;
        return;
      }
      // Mount Files while its requested document loads.
      openStage("files");
      openSourceInFilesStage({
        request: resolved.request,
        rootLabelFor: (rootId) =>
          project.roots.find((root) => root.id === rootId)?.label ?? rootId,
        navigate: () => openStage("files"),
      });
    })().catch((err) => {
      itemFileOpened = false;
      console.debug("[item-window] open file failed", err);
    });
  });

  let itemContextOpened = false;
  createEffect(() => {
    if (
      itemContextOpened ||
      itemSubject?.kind !== "context" ||
      appStore.state.sidecarStatus !== "connected"
    ) {
      return;
    }
    // Registry hydration retriggers peer project binding.
    if (!projects.byId(itemSubject.projectId)) return;
    itemContextOpened = true;
    if (shell.state.activeProjectId !== itemSubject.projectId) {
      shell.beginStageSwitch({
        projectId: itemSubject.projectId,
        kind: "cross-project",
      });
    }
    openStage(itemSubject.stageId as ContextNavItemId);
  });

  const sessionWindowViewNumbers = createMemo(() => {
    const numbers = new Map<string, number[]>();
    for (const view of itemWindowViews()) {
      if (view.kind !== "session" || !view.sessionId) continue;
      const existing = numbers.get(view.sessionId) ?? [];
      existing.push(view.viewNumber);
      numbers.set(view.sessionId, existing);
    }
    return numbers;
  });
  const sessionWindowCounts = createMemo(() =>
    new Map([...sessionWindowViewNumbers()].map(([sessionId, numbers]) => [sessionId, numbers.length])));
  const sessionWindowIds = createMemo(
    () => new Set(sessionWindowViewNumbers().keys()),
  );
  /** Context entries with one or more live peer windows for this project, by view number. */
  const contextWindowViewNumbers = createMemo(() => {
    const projectId = activeProjectId();
    const numbers = new Map<ContextNavItemId, number[]>();
    for (const view of itemWindowViews()) {
      if (
        view.kind !== "context" ||
        view.projectId !== projectId ||
        !view.stageId ||
        !isContextNavItemId(view.stageId)
      ) continue;
      const existing = numbers.get(view.stageId) ?? [];
      existing.push(view.viewNumber);
      numbers.set(view.stageId, existing);
    }
    return numbers;
  });
  const detachedStageIds = createMemo(() => new Set(contextWindowViewNumbers().keys()));
  const stageInPeerWindow = (stageId: ContextNavItemId | null) =>
    stageId != null && detachedStageIds().has(stageId);
  const projectPeerViews = createMemo(() => {
    const projectId = activeProjectId();
    return itemWindowViews().filter((view) => view.projectId === projectId);
  });

  /** Whether the main window, seen from a peer window, presents the subject. */
  const mainPresentsSubject = (subject: PeerOpenSubject | null): boolean => {
    if (!subject) return false;
    if (subject.kind === "file") {
      return editorDocumentParticipantsFor(
        subject.projectId,
        subject.rootId,
        subject.path,
      ).includes("window:main");
    }
    return mainWorkspaceViewPresents(
      subject.kind === "session"
        ? { kind: "session", projectId: subject.projectId, sessionId: subject.sessionId }
        : { kind: "stage", projectId: subject.projectId, stageId: subject.stageId },
    );
  };

  /** Other windows for the focused subject — switcher data, not a fact. */
  const focusedPeerViews = createMemo(() => {
    const subject = focusedPeerSubject();
    return peerWindowsForSubject(itemWindowViews(), subject, {
      selfClientId: clientIdentity(),
      mainPresents: mainPresentsSubject(subject),
    });
  });

  const sessionPeer = (sessionId: string, viewNumber?: number) =>
    itemWindowViews().find((view) =>
      view.kind === "session" && view.sessionId === sessionId &&
      (viewNumber === undefined || view.viewNumber === viewNumber));
  /** A session peer window presents its own chat; go-to-chat focuses it here. */
  const isThisSessionWindow = (sessionId: string) =>
    itemSubject?.kind === "session" && itemSubject.sessionId === sessionId;
  const stagePeer = (stageId: ContextNavItemId, viewNumber?: number) => {
    const projectId = activeProjectId();
    return itemWindowViews().find((view) =>
      view.kind === "context" && view.projectId === projectId && view.stageId === stageId &&
      (viewNumber === undefined || view.viewNumber === viewNumber));
  };
  const filePeer = (path: string, rootId?: string) => {
    const projectId = activeProjectId();
    return itemWindowViews().find((view) =>
      view.kind === "file" && view.projectId === projectId &&
      view.path === path && (rootId === undefined || view.rootId === rootId));
  };
  const raise = (peer: { label: string } | undefined) => { if (peer) void focusItemWindow(peer.label); };
  const close = (peer: { label: string } | undefined) => { if (peer) void closeItemWindow(peer.label); };

  const focusWindowByNumber = async (num: number): Promise<boolean> => {
    const label = windowLabelForNumber(projectPeerViews(), num);
    if (!label) return false;
    await focusItemWindow(label);
    return true;
  };

  const cycleNextPeerWindow = async (): Promise<boolean> => {
    const label = nextWindowLabel(projectPeerViews(), currentWindowNumber(itemSubject));
    if (!label) return false;
    await focusItemWindow(label);
    return true;
  };

  const detachStageToWindow = async (stageId: ContextNavItemId) => {
    const projectId = activeProjectId();
    if (!projectId || !stageAvailable(stageId)) return;
    const project = activeProject();
    const name = project?.name?.trim() || "Untitled project";
    try {
      await openItemWindow({
        kind: "context",
        projectId,
        stageId,
        title: `${stageLabelFor(stageId)} — ${name}`,
      });
    } catch (err) {
      console.debug("[item-window] open context failed", err);
    }
  };

  const openFocusedSubjectInNewWindow = async () => {
    const subject = focusedPeerSubject();
    if (!subject || !isTauriRuntime()) return;
    try {
      if (subject.kind === "session") {
        await openItemWindow({
          kind: "session",
          projectId: subject.projectId,
          sessionId: subject.sessionId,
          title: subject.title,
        });
        return;
      }
      if (subject.kind === "file") {
        await openItemWindow({
          kind: "file",
          projectId: subject.projectId,
          rootId: subject.rootId,
          path: subject.path,
          title: subject.title,
        });
        return;
      }
      await detachStageToWindow(subject.stageId);
    } catch (err) {
      console.debug("[item-window] open focused subject failed", err);
    }
  };

  const openFileWindow = async (file: {
    rootId: string;
    path: string;
    title: string;
    position?: ItemWindowDropPosition;
  }) => {
    const projectId = activeProjectId();
    if (!projectId) throw new Error("No project is bound to the file.");
    await openItemWindow({
      kind: "file",
      projectId,
      ...file,
    });
  };

  const openFileInNewWindow = async (
    key: FileBufferKey,
    position?: ItemWindowDropPosition,
  ) => {
    const projectId = activeProjectId();
    if (!projectId) throw new Error("No project is bound to the file tab.");
    const buffer = projectFilesState(projectId).byKey[key];
    if (!buffer) throw new Error("The file tab is no longer open.");
    await openFileWindow({
      rootId: buffer.rootId,
      path: buffer.path,
      title: buffer.name,
      position,
    });
  };

  const beginFileWindowDrag = async (
    key: string,
    position: ItemWindowDropPosition,
  ): Promise<string> => {
    const projectId = activeProjectId();
    if (!projectId) throw new Error("No project is bound to the file tab.");
    const buffer = projectFilesState(projectId).byKey[key];
    if (!buffer) throw new Error("The file tab is no longer open.");
    return await beginFileItemWindowDrag({
      kind: "file",
      projectId,
      rootId: buffer.rootId,
      path: buffer.path,
      title: buffer.name,
      position,
    });
  };

  const closeStageWindows = (stageId: ContextNavItemId) => {
    const projectId = activeProjectId();
    for (const view of itemWindowViews()) {
      if (
        view.kind === "context" &&
        view.projectId === projectId &&
        view.stageId === stageId
      ) {
        void closeItemWindow(view.label);
      }
    }
  };

  return {
    focusedPeerSubject, detachStageToWindow, openFocusedSubjectInNewWindow, openFileWindow,
    openFileInNewWindow, beginFileWindowDrag, closeStageWindows,
    sessionWindowIds, sessionWindowCounts, sessionWindowViewNumbers, contextWindowViewNumbers,
    detachedStageIds, stageInPeerWindow, projectPeerViews, focusedPeerViews,
    focusWindowByNumber, cycleNextPeerWindow, filePeer, sessionPeer, isThisSessionWindow,
    raiseSessionWindow: (sessionId: string, viewNumber?: number) => raise(sessionPeer(sessionId, viewNumber)),
    closeSessionWindow: (sessionId: string, viewNumber: number) => close(sessionPeer(sessionId, viewNumber)),
    raiseStageWindow: (stageId: ContextNavItemId, viewNumber?: number) => raise(stagePeer(stageId, viewNumber)),
    closeStageWindow: (stageId: ContextNavItemId, viewNumber: number) => close(stagePeer(stageId, viewNumber)),
    raiseFileWindow: (path: string, rootId?: string) => raise(filePeer(path, rootId)),
  };
}
