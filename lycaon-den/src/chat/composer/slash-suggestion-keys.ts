import { isImeComposition } from "../../platform/interaction/ime-composition.ts";

export type SlashSuggestionList = {
  count: number;
  active: () => number;
  setActive: (index: number) => void;
  choose: (index: number) => void;
  dismiss: () => void;
};

/**
 * Moves through and chooses the composer's slash suggestions. Returns whether
 * the key was consumed; modified keys and input method composition keys are not.
 */
export function handleSlashSuggestionKey(event: KeyboardEvent, list: SlashSuggestionList): boolean {
  if (list.count === 0 || isImeComposition(event) || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) {
    return false;
  }
  switch (event.key) {
    case "ArrowDown":
      list.setActive((list.active() + 1) % list.count);
      break;
    case "ArrowUp":
      list.setActive((list.active() - 1 + list.count) % list.count);
      break;
    case "Home":
      list.setActive(0);
      break;
    case "End":
      list.setActive(list.count - 1);
      break;
    case "Enter":
      list.choose(list.active());
      break;
    case "Escape":
      list.dismiss();
      break;
    default:
      return false;
  }
  event.preventDefault();
  event.stopPropagation();
  return true;
}
