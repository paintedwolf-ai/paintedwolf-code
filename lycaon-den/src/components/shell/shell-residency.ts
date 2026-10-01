import { createEffect, createMemo, createSignal } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { rememberCompanionStage } from "../../shell/layout-store.ts";
import { isHomePresentation, type ProjectPresentation } from "../../shell/project-presentation.ts";
import { useStageLayoutPresentation } from "../../shell/stage-layout-presentation.ts";
import type { resolveStageColumn } from "../../shell/stage-placement.ts";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import {
  SURFACE_HOME, SURFACE_SETTINGS, chatSessionKey, chatsSurfaceKey, projectConfigSurfaceKey,
  retainChatSurface, retainStageSurface, stageSurfaceKey, useResidentStack
} from "../../ui/resident-surfaces.ts";
import { useStageHandoff } from "../../ui/surface-reveal.ts";
import type { ShellScope } from "./shell-scope.ts";

type ResidencyDependencies = Pick<ShellScope, "activeProjectId" | "nav"> & {
  showHome: () => boolean;
  placeableStageId: () => ContextNavItemId | null;
  stageColumn: () => ReturnType<typeof resolveStageColumn>;
  showChatTabRail: () => boolean;
  presentedChat: () => ActiveChat | null;
  workspaceMounted: () => boolean;
  projectPresentation: () => ProjectPresentation;
};

export function createShellResidency({ activeProjectId, nav, showHome, placeableStageId,
  stageColumn, showChatTabRail, presentedChat, workspaceMounted, projectPresentation }: ResidencyDependencies) {
  const splitLive = () => stageColumn().splitLive;
  const stageColumnStageId = () => stageColumn().stageId;
  const requestedStageSurfaceKey = (): string | null => {
    const projectId = activeProjectId();
    const stage = stageColumnStageId();
    if (stage) return stageSurfaceKey(projectId, stage);
    if (nav() === "settings") return SURFACE_SETTINGS;
    if (nav() === "context" && projectId) {
      return projectConfigSurfaceKey(projectId);
    }
    if (nav() === "chats" && projectId) return chatsSurfaceKey(projectId);
    if (showHome()) return SURFACE_HOME;
    return null;
  };
  const [publishedStageSurfaces, setPublishedStageSurfaces] = createSignal<
    ReadonlySet<string>
  >(new Set());
  const stageOpeningOverChat = () => {
    const key = requestedStageSurfaceKey();
    return Boolean(
      key && !splitLive() && !publishedStageSurfaces().has(key),
    );
  };

  createEffect(() => {
    const stage = placeableStageId();
    const projectId = activeProjectId();
    if (!stage || !projectId) return;
    void rememberCompanionStage(stage);
  });

  const chatActive = createMemo(
    (previous: ActiveChat | null | undefined): ActiveChat | null => {
      if (showChatTabRail()) return presentedChat();
      if (splitLive()) return presentedChat();
      if (
        stageOpeningOverChat() &&
        previous?.projectId === activeProjectId()
      ) {
        return previous;
      }
      return null;
    },
  );
  const chatSurfaceKey = () => {
    const chat = chatActive();
    return chat ? chatSessionKey(chat.projectId, chat.sessionId) : null;
  };
  const chatStack = useResidentStack(chatSurfaceKey, {
    retain: (key) =>
      retainChatSurface(key, activeProjectId()),
  });
  const presentedChatCurrent = () => {
    if (!workspaceMounted()) return null;
    const chat = chatActive();
    if (!chat) return null;
    return {
      value: chat,
      pending:
        chatStack.presence(chatSessionKey(chat.projectId, chat.sessionId)) ===
        "pending",
    };
  };
  // The stage remains visible while chat prepares.
  const stageHandoff = useStageHandoff(
    stageColumnStageId,
    () => presentedChatCurrent(),
    showChatTabRail,
  );
  const presentedStageHandoff = () =>
    isHomePresentation(projectPresentation()) || workspaceMounted()
      ? stageHandoff()
      : null;
  const presentedSplitLive = () => presentedStageColumn().splitLive;
  const showNonChatStage = () => !showChatTabRail();
  const showStageColumn = () =>
    presentedStageHandoff() != null || showNonChatStage();
  const stageSurfaceActiveKey = (): string | null => {
    const projectId = activeProjectId();
    const stage = presentedStageHandoff();
    if (stage) return stageSurfaceKey(projectId, stage);
    if (nav() === "settings") return SURFACE_SETTINGS;
    if (nav() === "context" && projectId) {
      return projectConfigSurfaceKey(projectId);
    }
    if (nav() === "chats" && projectId) return chatsSurfaceKey(projectId);
    if (showHome()) return SURFACE_HOME;
    return null;
  };
  const stageStack = useResidentStack(stageSurfaceActiveKey, {
    retain: (key) =>
      retainStageSurface(key, activeProjectId()),
  });
  const presentedStageColumn = useStageLayoutPresentation({
    projectId: () => activeProjectId(),
    mounted: workspaceMounted,
    column: stageColumn,
    requestedSurface: stageSurfaceActiveKey,
    displayedSurface: stageStack.displayed,
  });
  const markStageSurfaceReady = (key: string) => {
    stageStack.markReady(key);
    setPublishedStageSurfaces((current) => {
      if (current.has(key)) return current;
      return new Set(current).add(key);
    });
  };
  const stageColDual = () =>
    !presentedSplitLive() &&
    ((Boolean(chatStack.pending()) &&
      (Boolean(presentedChatCurrent()) || showNonChatStage())) ||
      (presentedStageHandoff() != null && Boolean(presentedChatCurrent())));
  const chatColDual = () => chatStack.pending() != null;

  return {
    chatStack, stageStack, presentedChatCurrent, stageHandoff, stageOpeningOverChat,
    presentedSplitLive, showStageColumn,
    presentedStageColumn, markStageSurfaceReady, stageColDual, chatColDual
  };
}
