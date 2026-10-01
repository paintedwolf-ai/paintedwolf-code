import { createEffect } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { clearSourceReturn, returnToSource, sourceReturn } from "../../files/source/source-return.ts";
import { workspaceContextEqual, workspaceContextIsOpen, type WorkspaceContext } from "../../platform/windows/workspace-view-registry.ts";
import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import { hiddenSplitPanePref } from "../../shell/layout-store.ts";
import { isHomePresentation, type ProjectPresentation } from "../../shell/project-presentation.ts";
import { routedReturnOffer, type RoutedOrigin } from "../../shell/routed-return.ts";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import type { SplitColumnsOnScreen } from "../../shell/stage-placement.ts";
import { stageDefinition, stageLabelFor } from "../stage/stage-registry.tsx";
import type { StageBack } from "./StageBackChip.tsx";
import type { Nav, ShellNav, ShellNavigation } from "./shell-navigation-state.ts";
import type { ShellPaneVisibility } from "./shell-pane-visibility.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellWorkerDrawer } from "./shell-worker-drawer.ts";

/** Full-stage panels outside the stage registry, as workspace contexts. */
const SHELL_PANEL_CONTEXTS: Record<
  Exclude<ShellNav, "projects">,
  { panelId: string; label: string; projectScoped: boolean }
> = {
  settings: { panelId: "settings", label: "Settings", projectScoped: false },
  context: {
    panelId: "configuration",
    label: "Configuration",
    projectScoped: true,
  },
  chats: {
    panelId: "all-chats",
    label: "All chats",
    projectScoped: true,
  },
};

type WorkspaceContextDependencies = Pick<ShellScope, "activeProjectId" | "activeChat" | "nav" | "setNav" | "splitLive">
  & Pick<ShellNavigation, "routedReturn"> & Pick<ShellPaneVisibility, "showConversation">
  & Pick<ShellWorkerDrawer, "closeWorkers"> & {
  presentedChat: () => ActiveChat | null;
  projectPresentation: () => ProjectPresentation;
  conversationShown: () => boolean;
  splitColumns: () => SplitColumnsOnScreen;
  stageColumnStageId: () => ContextNavItemId | null;
  splitFitClaim: Pick<ShellPaneVisibility["splitFitClaim"], "claim">;
  /** The stage handing off into the stage column, whose titlebar carries its back chip. */
  stageHandoff: () => ContextNavItemId | null;
  /** Routes back to the Files stage after a source return reopens its location. */
  reopenFiles: () => void;
};

/** What this window shows as workspace contexts, and the back chips that return to routed origins. */
export function createShellWorkspaceContext({ activeProjectId, presentedChat,
  activeChat, projectPresentation, conversationShown, splitColumns,
  stageColumnStageId, nav, setNav, routedReturn, splitLive,
  showConversation, splitFitClaim, stageHandoff, closeWorkers, reopenFiles }: WorkspaceContextDependencies) {
  const stageWorkspaceContext = (stageId: ContextNavItemId): WorkspaceContext => {
    const projectId = activeProjectId();
    return { kind: "stage", ...(projectId ? { projectId } : {}), stageId };
  };

  /** The conversation column's surface: a chat, Home, or the project's empty chat. */
  const conversationSurface = (): RoutedOrigin | null => {
    const chat = presentedChat() ?? activeChat();
    if (chat) {
      return {
        context: {
          kind: "session",
          projectId: chat.projectId,
          sessionId: chat.sessionId,
        },
        label: "chat",
      };
    }
    if (isHomePresentation(projectPresentation())) {
      return { context: { kind: "panel", panelId: "home" }, label: "Home" };
    }
    const projectId = activeProjectId();
    return projectId
      ? {
        context: { kind: "panel", panelId: "project", projectId },
        label: "chat",
      }
      : null;
  };

  /** What a navigation value presents in this window, read without navigating there. */
  const workspaceSurfaceFor = (value: Nav): RoutedOrigin | null => {
    if (value === "projects") return conversationSurface();
    if (isContextNavItemId(value)) {
      return { context: stageWorkspaceContext(value), label: stageLabelFor(value) };
    }
    const projectId = activeProjectId();
    const panel = SHELL_PANEL_CONTEXTS[value];
    if (panel.projectScoped && !projectId) return null;
    return {
      context: {
        kind: "panel",
        panelId: panel.panelId,
        ...(panel.projectScoped && projectId ? { projectId } : {}),
      },
      label: panel.label,
    };
  };

  /** Contexts legible in this window. */
  const shownWorkspaceContexts = (): WorkspaceContext[] => {
    const shown: WorkspaceContext[] = [];
    const add = (context: WorkspaceContext | undefined) => {
      if (context && !shown.some((row) => workspaceContextEqual(row, context))) {
        shown.push(context);
      }
    };
    if (conversationShown()) add(conversationSurface()?.context);
    if (splitColumns().stage) {
      const stageId = stageColumnStageId();
      if (stageId) add(stageWorkspaceContext(stageId));
      else if (nav() !== "projects") add(workspaceSurfaceFor(nav())?.context);
    }
    return shown;
  };

  /** Returns to a routed origin; a conversation the split keeps off screen is brought back. */
  const returnFrom = (from: Nav) => {
    setNav(from);
    if (from !== "projects" || !splitLive()) return;
    if ((hiddenSplitPanePref() === "conversation")) void showConversation();
    else if (!splitColumns().conversation) splitFitClaim.claim();
  };

  /** Back chip for `stage`, or null when the visit was direct or its origin is already visible. */
  const backFor = (stage: Nav, testId: string): StageBack | null => {
    const offer = routedReturnOffer(routedReturn(), stage, {
      origin: workspaceSurfaceFor,
      visible: workspaceContextIsOpen,
    });
    return offer
      ? { label: offer.label, onBack: () => returnFrom(offer.from), testId }
      : null;
  };

  /** A source line opened from Files returns there; it outranks every stage back chip. */
  const sourceBack = (): StageBack | null => {
    const entry = sourceReturn();
    if (!entry || entry.projectId !== activeProjectId()) return null;
    return { label: `Back to ${entry.path}:${entry.line}`, onBack: () => {
      closeWorkers();
      void returnToSource().then(opened => { if (opened) reopenFiles(); });
    }, testId: "line-facts-back" };
  };
  createEffect(() => {
    const entry = sourceReturn();
    if (entry && entry.projectId !== activeProjectId()) clearSourceReturn();
  });

  /** Back chrome for the Shell's reserved titlebar band. */
  const titlebarBack = (): StageBack | null => {
    const source = sourceBack();
    if (source) return source;
    const stage = stageHandoff();
    if (!stage || stageDefinition(stage).backPlacement !== "titlebar") {
      return null;
    }
    return backFor(stage, `${stage}-back`);
  };

  /** Back chrome a stage renders itself. */
  const stageBack = (stage: ContextNavItemId): StageBack | null =>
    sourceBack() || stageDefinition(stage).backPlacement !== "stage"
      ? null
      : backFor(stage, `${stage}-back`);

  return { shownWorkspaceContexts, backFor, sourceBack, titlebarBack, stageBack };
}
