import { onCleanup } from "solid-js";

/** Marks a summary control that keeps its own action. */
const DISCLOSURE_ACTION_ATTRIBUTE = "data-disclosure-action";

const CONTROL_SELECTOR =
  "a[href], button, input, select, textarea, summary, [role='button'], [role='link'], [contenteditable='true']";

/**
 * Makes a disclosure's summary one click target: a click anywhere in it goes to the
 * toggle unless it lands on a declared action.
 */
export function bindDisclosureSummary(summary: HTMLElement, toggle: () => HTMLElement | undefined): void {
  summary.setAttribute("data-disclosure-summary", "");
  const route = (event: MouseEvent) => {
    const target = toggle();
    if (!target || !(event.target instanceof Element)) return;
    const found = event.target.closest(CONTROL_SELECTOR);
    const control = found && summary.contains(found) ? found : null;
    if (control === target || control?.closest(`[${DISCLOSURE_ACTION_ATTRIBUTE}]`)) return;
    event.preventDefault();
    event.stopPropagation();
    target.click();
  };
  summary.addEventListener("click", route, { capture: true });
  onCleanup(() => summary.removeEventListener("click", route, { capture: true }));
}
