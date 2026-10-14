import { findController } from "./find-controller.ts";
import { gotoLineController } from "./goto-line-controller.ts";

export const FIND_BAR_ID = "den-find-bar";
export const GOTO_LINE_BAR_ID = "den-goto-line-bar";

/** Toolbar buttons that open an inline command bar are disclosures, not popup menus. */
export function findBarDisclosure(): { expanded: boolean; controls: string | undefined } {
  const expanded = findController.isOpen();
  return { expanded, controls: expanded ? FIND_BAR_ID : undefined };
}

export function gotoLineBarDisclosure(): { expanded: boolean; controls: string | undefined } {
  const expanded = gotoLineController.isOpen();
  return { expanded, controls: expanded ? GOTO_LINE_BAR_ID : undefined };
}
