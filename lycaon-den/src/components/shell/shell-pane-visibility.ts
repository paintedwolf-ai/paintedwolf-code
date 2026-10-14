import { createEffect, createSignal, untrack } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { widenWindowBy, windowGeometryClaimPending } from "../../platform/windows/window-chrome.ts";
import type { WindowSubject } from "../../platform/windows/window-subject.ts";
import {
  hiddenSplitPanePref,
  commitHiddenSplitPane,
  commitNavCollapsedPref,
  conversationHidePreviewed,
  effectiveNavWidthPx,
  layoutViewportWidthPx,
  narrowSurvivorPref,
  navCollapsedPref,
  navHidePreviewed,
  requestedChatWidthPx,
  preferredNavWidthPx,
  setSplitFocusRegion,
  splitOrderPref,
  workspaceOrientationPref,
} from "../../shell/workspace-layout.ts";
import { createSplitPaneVisibility } from "../../shell/split-pane-visibility.ts";
import { createPaneVisibilityIntent } from "../../shell/pane-visibility-intent.ts";
import { shouldAutomaticallyCollapseNav, stageOpenWindowDeficitPx, workspaceStageFloorPx } from "../../shell/responsive-collapse.ts";
import { createWindowClaim } from "../../shell/window-claim.ts";
import { workspacePaneOrder } from "../../shell/workspace-pane-order.ts";
import { CHAT_COL_MIN, DEFAULT_SPLIT_COMPANION } from "../../shell/stage-placement.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import type { ChatTabRailProps } from "../nav/ChatTabRail.tsx";
import type { StageEdgePanes } from "../nav/StageEdgeControls.tsx";
import { stageLabelFor } from "../stage/stage-registry.tsx";

type PaneVisibilityDependencies = {
  windowSubject: WindowSubject | null;
  /** The stage column resolves to a split this frame. */
  splitLive: () => boolean;
  /** The split the window presents, which trails `splitLive` through a handoff. */
  presentedSplitLive: () => boolean;
  presentedStageId: () => ContextNavItemId | null;
  /** The stage column holds content; otherwise a lone column is the chat. */
  stageColumnOccupied: () => boolean;
};

/** A column whose title bar can draw one window edge's controls. */
export type WindowEdgeHost = "nav" | "stage" | "chat";

/** Which column draws one window edge's controls now, and once the narrow band drops a split column. */
export type WindowEdgeHosts = { owner: WindowEdgeHost; narrow: WindowEdgeHost };

export type ShellPaneVisibility = ReturnType<typeof createShellPaneVisibility>;

/** Pane visibility shares width claims with the controls that reopen each pane. */
export function createShellPaneVisibility({ windowSubject, splitLive, presentedSplitLive,
  presentedStageId, stageColumnOccupied }: PaneVisibilityDependencies) {
  const [fileWindowNavCollapsed, setFileWindowNavCollapsed] = createSignal(
    windowSubject?.kind === "file",
  );
  const [navAutomaticallyCollapsed, setNavAutomaticallyCollapsed] =
    createSignal(false);
  const windowNavUserCollapsed = () =>
    windowSubject?.kind === "file"
      ? fileWindowNavCollapsed()
      : navCollapsedPref();
  const windowNavCollapsed = () =>
    windowNavUserCollapsed() || navAutomaticallyCollapsed();
  const setWindowNavCollapsed = (collapsed: boolean) =>
    windowSubject?.kind === "file"
      ? setFileWindowNavCollapsed(collapsed)
      : commitNavCollapsedPref(collapsed);
  /** Nav slot width; declared on each reader (`inherits: false` in global.css). */
  const navSlotWidthCss = () =>
    windowNavCollapsed() || navHidePreviewed()
      ? "0px"
      : `${effectiveNavWidthPx()}px`;

  const responsiveStageFloorPx = () =>
    presentedSplitLive() && hiddenSplitPanePref() === "stage" ? CHAT_COL_MIN : workspaceStageFloorPx({
      splitLive: presentedSplitLive(),
      stageId: presentedStageId(),
    });

  // Width changes do not subscribe to the collapse preference.
  let navWasAutomaticallyCollapsed = false;
  createEffect(() => {
    const viewportWidthPx = layoutViewportWidthPx();
    const navWidthPx = preferredNavWidthPx();
    const live = presentedSplitLive();
    const claiming = windowGeometryClaimPending();
    if (untrack(windowNavUserCollapsed)) {
      navWasAutomaticallyCollapsed = false;
      setNavAutomaticallyCollapsed(false);
      return;
    }
    // Pending window size suspends collapse recalculation.
    if (claiming) return;
    const shouldCollapse = shouldAutomaticallyCollapseNav({
      automaticCollapsed: navWasAutomaticallyCollapsed,
      viewportWidthPx,
      navWidthPx,
      stageMinWidthPx: responsiveStageFloorPx(),
      splitColumns: live && hiddenSplitPanePref() === null,
      chatWidthPx: requestedChatWidthPx(),
    });
    const automatic = windowSubject == null && shouldCollapse;
    navWasAutomaticallyCollapsed = automatic;
    setNavAutomaticallyCollapsed(automatic);
  });

  const {
    show: showWindowNav,
    hide: hideWindowNav,
    toggle: toggleWindowNav,
  } = createPaneVisibilityIntent({
    collapsed: windowNavCollapsed,
    widen: () =>
      widenWindowBy(
        stageOpenWindowDeficitPx({
          viewportWidthPx: layoutViewportWidthPx(),
          navWidthPx: preferredNavWidthPx(),
          countNavWidth: true,
          stageMinWidthPx: responsiveStageFloorPx(),
          splitColumns: splitLive() && hiddenSplitPanePref() === null,
          chatWidthPx: requestedChatWidthPx(),
        }),
      ),
    paintOpen: () => {
      navWasAutomaticallyCollapsed = false;
      setNavAutomaticallyCollapsed(false);
      setWindowNavCollapsed(false);
    },
    paintClosed: () => {
      navWasAutomaticallyCollapsed = false;
      setNavAutomaticallyCollapsed(false);
      setWindowNavCollapsed(true);
    },
  });

  /** Split width includes the current columns and visible sidebar. */
  const splitFitDeficitPx = () =>
    windowSubject != null
      ? 0
      : stageOpenWindowDeficitPx({
          viewportWidthPx: layoutViewportWidthPx(),
          navWidthPx: preferredNavWidthPx(),
          countNavWidth: !windowNavCollapsed(),
          stageMinWidthPx: workspaceStageFloorPx({ splitLive: true, stageId: presentedStageId() }),
          splitColumns: true,
          chatWidthPx: requestedChatWidthPx(),
        });

  const splitVisibility = createSplitPaneVisibility({
    hidden: hiddenSplitPanePref,
    widen: () => widenWindowBy(splitFitDeficitPx()),
    commit: commitHiddenSplitPane,
    onHide: (pane) => {
      setSplitFocusRegion(pane === "stage" ? "chat" : "stage");
      queueMicrotask(() => focusRegion(pane === "stage" ? "composer" : "context"));
    },
  });
  const showConversation = () => splitVisibility.show("conversation");
  const hideConversation = () => splitVisibility.hide("conversation");
  const toggleConversation = () => splitVisibility.toggle("conversation");
  const showContext = () => splitVisibility.show("stage");
  const hideContext = () => splitVisibility.hide("stage");
  const toggleContext = () => splitVisibility.toggle("stage");
  const contextHidden = () => presentedSplitLive() && hiddenSplitPanePref() === "stage";

  // Both seam controls share one claim, and therefore its refusals.
  const splitFitClaim = createWindowClaim({
    deficitPx: splitFitDeficitPx,
    rearmOn: layoutViewportWidthPx,
  });

  /** An explicit request to see the conversation shows a split's hidden column. */
  const revealConversation = (): Promise<boolean> =>
    splitLive() && (hiddenSplitPanePref() === "conversation")
      ? showConversation()
      : Promise.resolve(true);

  /** The split's conversation, hidden or following a divider drag toward hidden. */
  const conversationHidden = (): "preview" | "hidden" | null => {
    if (!presentedSplitLive()) return null;
    if (hiddenSplitPanePref() === "conversation") return "hidden";
    return conversationHidePreviewed() ? "preview" : null;
  };

  const order = () => workspacePaneOrder(workspaceOrientationPref(), splitOrderPref());
  const stageIsLeading = () => !presentedSplitLive() || order().stageOnLeft;
  const navTouchesChat = () => !presentedSplitLive() || contextHidden() || order().chatBesideNav;
  const navSide = () => order().navSide;
  const conversationSide = () => order().conversationSide;
  /** Which column header sits under the overlay window controls. */
  const navLeadsWindow = () => navSide() === "left" && !windowNavCollapsed();
  const stageHeaderLeadsWindow = () =>
    !navLeadsWindow() &&
    (stageIsLeading() || conversationHidden() === "hidden");
  const chatHeaderLeadsWindow = () =>
    !navLeadsWindow() && (!presentedSplitLive() || !stageIsLeading() || contextHidden());

  /** The column against a window edge draws that edge's window controls. */
  const windowEdgeHosts = (edge: "left" | "right"): WindowEdgeHosts => {
    if (navSide() === edge && !windowNavCollapsed()) return { owner: "nav", narrow: "nav" };
    if (!presentedSplitLive()) {
      const host = stageColumnOccupied() ? "stage" : "chat";
      return { owner: host, narrow: host };
    }
    if (contextHidden()) return { owner: "chat", narrow: "chat" };
    if (conversationHidden() === "hidden") return { owner: "stage", narrow: "stage" };
    const stageSide = order().stageOnLeft ? "left" : "right";
    return {
      owner: stageSide === edge ? "stage" : "chat",
      // The stylesheet drops the other column below the narrow band.
      narrow: narrowSurvivorPref() === "stage" ? "stage" : "chat",
    };
  };
  /** One column's claims on both window edges. */
  const windowEdgeClaims = (host: WindowEdgeHost) =>
    (["left", "right"] as const).map((side) => {
      const hosts = windowEdgeHosts(side);
      return { side, owner: hosts.owner === host, narrow: hosts.narrow === host };
    });

  /** Hidden panes reopen from the stage title bar's window edges. */
  const stageEdges = (conversationAttention: StageEdgePanes["conversation"]["attention"]): StageEdgePanes => ({
    nav: {
      collapsed: windowNavCollapsed(),
      automatic: navAutomaticallyCollapsed(),
      onlyWhenNarrow: presentedSplitLive() && navTouchesChat() && conversationHidden() !== "hidden",
      side: navSide(),
      onExpand: () => void showWindowNav(),
    },
    context: presentedSplitLive() && hiddenSplitPanePref() === null ? { side: order().stageOnLeft ? "left" : "right", onHide: hideContext } : undefined,
    conversation: {
      collapsed: conversationHidden() === "hidden",
      side: conversationSide(),
      attention: conversationAttention,
      onExpand: () => void showConversation(),
      hiddenWhenNarrow:
        presentedSplitLive() &&
        hiddenSplitPanePref() === null &&
        narrowSurvivorPref() === "stage",
      onWiden: splitFitClaim.claim,
      widenRefusals: splitFitClaim.refusals,
    },
  });

  /** A showing split conversation hides, or claims width for its stage, from its seam. */
  const conversationSeam = (): ChatTabRailProps["conversationSeam"] =>
    presentedSplitLive() && hiddenSplitPanePref() !== "conversation"
      ? {
          side: conversationSide(),
          contextHidden: contextHidden(),
          onRestoreContext: () => void showContext(),
          onHide: hideConversation,
          stageLabel: stageLabelFor(presentedStageId() ?? DEFAULT_SPLIT_COMPANION),
          onWidenForStage: splitFitClaim.claim,
          widenRefusals: splitFitClaim.refusals,
        }
      : undefined;

  return {
    windowNavUserCollapsed, windowNavCollapsed, navAutomaticallyCollapsed, navSlotWidthCss,
    showWindowNav, hideWindowNav, toggleWindowNav,
    showConversation, hideConversation, toggleConversation, revealConversation, splitFitClaim,
    showContext, hideContext, toggleContext, contextHidden,
    conversationHidden, stageIsLeading, navTouchesChat, navSide, conversationSide,
    stageHeaderLeadsWindow, chatHeaderLeadsWindow, stageEdges, conversationSeam,
    windowEdgeHosts, windowEdgeClaims,
  };
}
