import type { SplitOrder, WorkspaceOrientation } from "../../shared/app-state-types.ts";

export type PaneSide = "left" | "right";

/** Split order is relative to the sidebar; mirroring reverses the whole workspace. */
export function workspacePaneOrder(
  orientation: WorkspaceOrientation,
  splitOrder: SplitOrder,
) {
  const navSide: PaneSide = orientation === "mirrored" ? "right" : "left";
  const chatBesideNav = splitOrder === "chat-first";
  const conversationSide: PaneSide = chatBesideNav
    ? navSide
    : navSide === "left" ? "right" : "left";
  return { navSide, conversationSide, chatBesideNav, stageOnLeft: conversationSide === "right" };
}
