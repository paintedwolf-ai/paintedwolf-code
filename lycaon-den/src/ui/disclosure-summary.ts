import { createRenderEffect, onCleanup } from "solid-js";

/** Marks a summary control that keeps its own action while its disclosure is closed. */
export const DISCLOSURE_ACTION_ATTRIBUTE = "data-disclosure-action";

const CONTROL_SELECTOR =
  "a[href], button, input, select, textarea, summary, [role='button'], [role='link'], [contenteditable='true']";

export type DisclosureSummaryOptions = {
  open: () => boolean;
  /** The button that opens and closes the disclosure. */
  toggle: () => HTMLElement | undefined;
};

/**
 * Makes a disclosure's summary one click target. While closed, a click anywhere in it
 * goes to the toggle unless it lands on a declared action. While open, the summary's
 * own controls act and a click elsewhere in it goes to the toggle.
 */
export function bindDisclosureSummary(summary: HTMLElement, options: DisclosureSummaryOptions): void {
  createRenderEffect(() => {
    summary.setAttribute("data-disclosure-summary", options.open() ? "open" : "closed");
  });
  const route = (event: MouseEvent) => {
    const toggle = options.toggle();
    if (!toggle || !(event.target instanceof Element)) return;
    const found = event.target.closest(CONTROL_SELECTOR);
    const control = found && summary.contains(found) ? found : null;
    if (control === toggle) return;
    if (control?.closest(`[${DISCLOSURE_ACTION_ATTRIBUTE}]`)) return;
    if (control && options.open()) return;
    event.preventDefault();
    event.stopPropagation();
    toggle.click();
  };
  summary.addEventListener("click", route, { capture: true });
  onCleanup(() => summary.removeEventListener("click", route, { capture: true }));
}
