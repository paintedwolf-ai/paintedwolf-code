/**
 * Map a sidebar chat pick to an outcome from visibility facts.
 * Shell controls what each outcome does.
 */

export type SidebarPick =
  /** Show it in this column. */
  | "reveal"
  /** Raise the peer window that already has it. */
  | "raise-peer"
  /** Bind it as the subject; leave the stage. */
  | "select-only"
  /** The subject is already this chat; show the column. */
  | "show-subject";

export type SidebarPickInput = {
  /** On screen in some live view — this window's conversation, a split, or a peer window. */
  onScreen: boolean;
  /** This window's layout holds a conversation column, shown or hidden. */
  conversationInLayout: boolean;
  /** A peer window is showing this chat. */
  hasPeerWindow: boolean;
  /** This row is the current subject. */
  isSubject: boolean;
};

export function resolveSidebarPick(input: SidebarPickInput): SidebarPick {
  if (input.onScreen) {
    if (input.conversationInLayout) return "reveal";
    if (input.hasPeerWindow) return "raise-peer";
    return "reveal";
  }
  return input.isSubject ? "show-subject" : "select-only";
}
