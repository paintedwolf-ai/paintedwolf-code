import { isImeComposition } from "../platform/interaction/ime-composition.ts";
import { handlerMatchesEvent } from "../shortcuts/display-binding-for.ts";
import { cycleCrossbarMode } from "./crossbar-model.ts";
import { SEARCH_MODE_JUMP_COMMANDS, type CrossbarMode } from "./crossbar-modes.ts";
import type { SearchMatchPrefs } from "./search-match-prefs.ts";

export type CrossbarMatchToggle = keyof Pick<SearchMatchPrefs, "caseSensitive" | "wholeWord" | "regex">;

const MATCH_TOGGLES: ReadonlyArray<{ commandId: string; key: CrossbarMatchToggle }> = [
  { commandId: "search.toggleMatchCase", key: "caseSensitive" },
  { commandId: "search.toggleWholeWord", key: "wholeWord" },
  { commandId: "search.toggleRegex", key: "regex" },
];

export type CrossbarPanelKeyActions = {
  /** Escape: leave a command flow, otherwise close the Crossbar. */
  dismiss: () => void;
  activeMode: () => CrossbarMode;
  selectMode: (mode: CrossbarMode) => void;
  toggleMatch: (key: CrossbarMatchToggle) => void;
  escalate: () => void;
};

/** The Crossbar dialog's own keys; keys that belong to an input method composition are left to it. */
export function handleCrossbarPanelKey(e: KeyboardEvent, actions: CrossbarPanelKeyActions): void {
  if (isImeComposition(e)) return;
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    actions.dismiss();
    return;
  }
  const step = handlerMatchesEvent("search.mode.next", e) ? 1 : handlerMatchesEvent("search.mode.prev", e) ? -1 : 0;
  if (step !== 0) {
    e.preventDefault();
    actions.selectMode(cycleCrossbarMode(actions.activeMode(), step));
    return;
  }
  const toggle = MATCH_TOGGLES.find(({ commandId }) => handlerMatchesEvent(commandId, e));
  if (toggle) {
    e.preventDefault();
    actions.toggleMatch(toggle.key);
    return;
  }
  const jump = SEARCH_MODE_JUMP_COMMANDS.find(({ commandId }) => handlerMatchesEvent(commandId, e));
  if (jump) {
    e.preventDefault();
    actions.selectMode(jump.mode);
    return;
  }
  if (handlerMatchesEvent("search.escalate", e)) {
    e.preventDefault();
    actions.escalate();
  }
}
