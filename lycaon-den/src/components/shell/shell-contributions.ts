import { type Nav } from "./shell-navigation-state.ts";
import { type ShellScope } from "./shell-scope.ts";
import { createShellPeerWindows } from "./shell-peer-windows.ts";
import { createEffect, createMemo, onCleanup, onMount } from "solid-js";
import { CONTEXT_NAV_CATALOG, type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { isChatActivityLive } from "../../chat/session/session-activity.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { type ShellFactState, setShellFactState } from "../../contributions/shell-facts.ts";
import { filesCommandContext } from "../../files/commands/files-command-context.ts";
import { mountedFilesEditorView } from "../../files/editor/files-editor-host.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { windowSubject } from "../../platform/windows/window-subject.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";
import { filesTreeCollapsed } from "../../files/tree/files-tree-window-state.ts";
import { hiddenSplitPanePref } from "../../shell/workspace-layout.ts";
import { invokeCommand, isComposerFocused, registerHostCommandInvoker } from "../../shortcuts/dispatcher.ts";
import { focusRegionsVersion, mountedFocusRegions } from "../../shortcuts/focus-region.ts";
import { workspaceKindOf, type PeerOpenSubject } from "../../shortcuts/peer-view-subject.ts";
import { selectedFileVersion } from "../../files/history/files-version-selection.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { stageFeatureEnabled, type StageFeatureFlags } from "../stage/stage-registry.tsx";
import { type ContributionCommand } from "../../api/types.ts";
import { runComposerPrefillEffect } from "../../chat/composer/add-to-chat.ts";
import { contributionCommand, dispatchContributionCommand, registerUIEffectSink, registerCommandContextProvider, reportDispatchFailure, crossbarCommands } from "../../contributions/dispatch.ts";
import { attachAppMenuCommands, invalidateAppMenuProjection, syncAppMenu } from "../../platform/desktop/app-menu.ts";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";

export function createShellContributions(options: Pick<ShellScope, "shell" | "appStore" | "activeChat" | "readyChat" | "nav"> & {
  windowNavCollapsed: () => boolean;
  contextHidden: () => boolean;
  splitLive: () => boolean;
  stageColumnStageId: () => ContextNavItemId | null;
  stageFeatureFlags: () => StageFeatureFlags;
  focusedPeerSubject: () => PeerOpenSubject | null;
  focusedPeerViews: ReturnType<typeof createShellPeerWindows>["focusedPeerViews"];
  openStageWithPlacement: (stage: ContextNavItemId, arrival: "routed" | "deliberate") => void;
  openCommand: (commandId: string) => void;
  windowFocused: () => boolean;
}) {
  const { activeChat, readyChat, nav, windowNavCollapsed, contextHidden, splitLive,
    stageColumnStageId, stageFeatureFlags, focusedPeerSubject, focusedPeerViews,
    openStageWithPlacement } = options;
  const itemSubject = windowSubject();
  const activeFileBuffer = () => {
    const projectId = options.shell.state.activeProjectId;
    const filesState = projectId != null ? projectFilesState(projectId) : null;
    const activeKey = filesState?.activeKey ?? null;
    return activeKey && filesState ? filesState.byKey[activeKey] ?? null : null;
  };
  // Host-validated destinations run the navigation command of the same name.
  const navigateContributionDestination = (destination: string) => {
    if (destination.startsWith("context.")) {
      invokeCommand(`go.${destination}`);
      return;
    }
    if (destination.startsWith("settings.")) {
      invokeCommand(`settings.section.${destination.slice("settings.".length)}`);
      return;
    }
    switch (destination) {
      case "files":
        openStageWithPlacement("files", "routed");
        return;
      case "home":
        invokeCommand("go.home");
        return;
      case "chat":
        invokeCommand("go.chat");
        return;
      case "composer":
        invokeCommand("go.composer");
        return;
      case "sidebar":
        invokeCommand("go.sidebar");
        return;
      case "all_chats":
        invokeCommand("go.allChats");
        return;
      default:
        return;
    }
  };
  // Command availability uses live shell facts; the host revalidates.
  const contributionShellView = (n: Nav): string => {
    if (n === "projects") return "welcome";
    if (n === "settings") return "settings";
    if (n === "chats") return "chat";
    if (n === "files") return "files";
    return "context";
  };
  const editorCommandTarget = () => {
    const projectId = options.shell.state.activeProjectId;
    const buffer = activeFileBuffer();
    if (!projectId || !buffer || buffer.kind !== "text" || stageColumnStageId() !== "files" ||
      selectedFileVersion(projectId, buffer.key) != null) return null;
    const view = mountedFilesEditorView(projectId, buffer.key);
    return view ? filesCommandContext(buffer, view) : null;
  };

  /** The Files stage shows a past version of its active buffer. */
  const filesVersionHistorical = (): boolean => {
    const projectId = options.shell.state.activeProjectId;
    const buffer = activeFileBuffer();
    return (
      projectId != null &&
      buffer != null &&
      stageColumnStageId() === "files" &&
      selectedFileVersion(projectId, buffer.key) != null
    );
  };

  /** The shell's typed state, in the closed fact vocabulary. */
  const currentShellFacts = (): ShellFactState => {
    const frame = contributionFrame();
    focusRegionsVersion();
    const chat = activeChat();
    const projectId = options.shell.state.activeProjectId;
    const subject = itemSubject;
    const regions = mountedFocusRegions();
    const flags = stageFeatureFlags();
    const activityLive = chat
      ? isChatActivityLive(options.appStore, chat.sessionId)
      : false;
    return {
      activeView: contributionShellView(nav()),
      mountedRegions: regions,
      workspaceKind: workspaceKindOf(subject),
      isPeerWorkspace: subject != null,
      contextFeatures: CONTEXT_NAV_CATALOG.filter((id) =>
        stageFeatureEnabled(id, flags),
      ),
      // Real textarea focus: the chat stage always has a composer mounted.
      composerFocused: isComposerFocused(),
      chatNavigable: subject == null && readyChat() != null,
      contextNavigable:
        subject == null ||
        subject.kind === "context" ||
        regions.includes("context"),
      sidebarExpanded: !windowNavCollapsed(),
      contextCollapsed: contextHidden(),
      conversationCollapsed: (hiddenSplitPanePref() === "conversation"),
      splitLive: splitLive(),
      filesStageActive: stageColumnStageId() === "files",
      filesTreeCollapsed: filesTreeCollapsed(),
      fileSummariesOn: fileSummariesEnabled(),
      fileSummariesKnown: fileSummariesSettingKnown(),
      filesVersionHistorical: filesVersionHistorical(),
      peerOpenAvailable:
        isTauriRuntime() && projectId != null && focusedPeerSubject() != null,
      peerViewsPresent: focusedPeerViews().length > 0,
      projectOpen: projectId != null,
      sessionExists: chat != null,
      sessionIdle: chat != null && !activityLive,
      activityLive,
      editor: editorCommandTarget()?.facts ?? null,
      requirementReady: (id) =>
        frame?.requirements.some((req) => req.id === id && req.ready) ?? false,
      configurationOn: (id) =>
        frame?.configuration.some(
          (property) => property.id === id && property.value === true,
        ) ?? false,
    };
  };

  onMount(() => {
    const unregisterEffects = registerUIEffectSink({
      navigate: navigateContributionDestination,
      composerPrefill: (text) => {
        void runComposerPrefillEffect(options.shell.state.activeProjectId, text);
      },
    });
    onCleanup(unregisterEffects);
  });
  onMount(() => {
    onCleanup(registerCommandContextProvider((projectId) =>
      projectId === options.shell.state.activeProjectId ? editorCommandTarget()?.context : undefined,
    ));
  });
  createEffect(() => {
    setShellFactState(currentShellFacts());
  });
  // Crossbar commands read the published fact state.
  const crossbarActions = createMemo<ContributionCommand[]>(() => {
    return crossbarCommands();
  });
  onMount(() => {
    const dispatchDeps = () => ({
      client: getLycaonClient(),
      projectId: options.shell.state.activeProjectId,
      sessionId: activeChat()?.sessionId ?? null,
    });
    const activateContribution = (commandId: string) => {
      const command = contributionCommand(commandId);
      if (command && (command.input?.length || command.interaction || command.result_treatment === "output")) {
        options.openCommand(commandId);
        return;
      }
      const projectId = options.shell.state.activeProjectId;
      void dispatchContributionCommand(commandId, dispatchDeps()).then((result) =>
        reportDispatchFailure(result, projectId, command?.title ?? commandId),
      );
    };
    // Menus and shortcuts share command forms and result presentation.
    onCleanup(attachAppMenuCommands(activateContribution));
    onCleanup(registerHostCommandInvoker(activateContribution));
  });
  createEffect(() => {
    // The focused window supplies the app menu context.
    if (!options.windowFocused()) {
      invalidateAppMenuProjection();
      return;
    }
    const overrides = shortcutOverrides();
    void syncAppMenu(Object.keys(overrides).length > 0 ? overrides : null);
  });

  return { crossbarActions };
}
