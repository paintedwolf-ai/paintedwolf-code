/** Resolves durable subjects for peer-view commands. */
import type { ContextNavItemId } from "../../shared/app-state-types.ts";
import type { ItemWindowView } from "../platform/windows/item-windows.ts";
import { editorWindow, type EditorWindow } from "../platform/windows/editor-windows.ts";
import type { WindowSubject } from "../platform/windows/window-subject.ts";

export type WorkspaceKind = "main" | "session" | "file" | "context";

export type PeerOpenSubject =
  | {
      kind: "session";
      projectId: string;
      sessionId: string;
      title: string;
    }
  | {
      kind: "file";
      projectId: string;
      rootId: string;
      path: string;
      title: string;
    }
  | {
      kind: "context";
      projectId: string;
      stageId: ContextNavItemId;
      title: string;
    };

export type PeerSubjectFacts = {
  windowSubject: WindowSubject | null;
  projectId: string | null;
  /** Ready/foreground chat in this workspace, when present. */
  session: { sessionId: string; title: string } | null;
  /** Context stage shown in the stage column. */
  contextStage: ContextNavItemId | null;
  contextTitle: string | null;
  /** Active Files buffer when an editor is present. */
  activeFile: {
    rootId: string;
    path: string;
    title: string;
  } | null;
  /** Files stage is showing without an active editor. */
  filesStageWithoutEditor: boolean;
  /** Split column that currently has keyboard focus. */
  splitFocusRegion: "stage" | "chat" | null;
  splitLive: boolean;
};

export function workspaceKindOf(
  subject: WindowSubject | null,
): WorkspaceKind {
  if (!subject) return "main";
  return subject.kind;
}

/** Resolves the focused peer-view subject. */
export function resolvePeerOpenSubject(
  facts: PeerSubjectFacts,
): PeerOpenSubject | null {
  const peer = facts.windowSubject;
  if (peer?.kind === "session") {
    return {
      kind: "session",
      projectId: peer.projectId,
      sessionId: peer.sessionId,
      title: facts.session?.title || "Chat",
    };
  }
  if (peer?.kind === "file") {
    return {
      kind: "file",
      projectId: peer.projectId,
      rootId: peer.rootId,
      path: peer.path,
      title: facts.activeFile?.title || peer.path.split("/").pop() || "File",
    };
  }
  if (peer?.kind === "context") {
    return {
      kind: "context",
      projectId: peer.projectId,
      stageId: peer.stageId as ContextNavItemId,
      title: facts.contextTitle || peer.stageId,
    };
  }

  const projectId = facts.projectId;
  if (!projectId) return null;

  const preferStage =
    facts.splitLive && facts.splitFocusRegion === "stage"
      ? true
      : facts.splitLive && facts.splitFocusRegion === "chat"
        ? false
        : facts.session == null;

  if (!preferStage && facts.session) {
    return {
      kind: "session",
      projectId,
      sessionId: facts.session.sessionId,
      title: facts.session.title || "Chat",
    };
  }

  if (facts.activeFile) {
    return {
      kind: "file",
      projectId,
      rootId: facts.activeFile.rootId,
      path: facts.activeFile.path,
      title: facts.activeFile.title,
    };
  }

  if (facts.filesStageWithoutEditor || facts.contextStage) {
    const stage = facts.contextStage ?? "files";
    return {
      kind: "context",
      projectId,
      stageId: stage,
      title: facts.contextTitle || stage,
    };
  }

  if (facts.session) {
    return {
      kind: "session",
      projectId,
      sessionId: facts.session.sessionId,
      title: facts.session.title || "Chat",
    };
  }

  return null;
}

/** Registry slice for the focused durable subject. */
export function peersForSubject(
  views: readonly ItemWindowView[],
  subject: PeerOpenSubject | null,
): ItemWindowView[] {
  if (!subject) return [];
  return views.filter((view) => {
    if (view.kind !== subject.kind || view.projectId !== subject.projectId) {
      return false;
    }
    if (subject.kind === "session") return view.sessionId === subject.sessionId;
    if (subject.kind === "file") {
      return view.rootId === subject.rootId && view.path === subject.path;
    }
    return view.stageId === subject.stageId;
  });
}

/** The caller supplies main-window presence because the item registry excludes it. */
export function peerWindowsForSubject(
  views: readonly ItemWindowView[],
  subject: PeerOpenSubject | null,
  presence: { selfClientId: string; mainPresents: boolean },
): EditorWindow[] {
  if (!subject) return [];
  const peers = peersForSubject(views, subject)
    .filter((view) => `window:${view.label}` !== presence.selfClientId)
    .flatMap((view) => {
      const window = editorWindow({ client_id: `window:${view.label}` }, views);
      return window ? [window] : [];
    });
  if (!presence.mainPresents || presence.selfClientId === "window:main") return peers;
  const main = editorWindow({ client_id: "window:main" }, views);
  return main ? [{ ...main, title: subject.title }, ...peers] : peers;
}
