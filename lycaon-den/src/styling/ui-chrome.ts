/**
 * Chrome root mark. `user-select` inherits. Form fields under a chrome root
 * stay selectable (`ui-chrome.css`).
 */

export const DEN_CHROME_ATTR = "data-den-chrome" as const;

/** Spread onto chrome root elements. */
export function chromeProps(): { [DEN_CHROME_ATTR]: "" } {
  return { [DEN_CHROME_ATTR]: "" };
}

export const DEN_PROSE_ATTR = "data-den-prose" as const;

/** Restores selection on copy that sits inside a chrome root. */
export function proseProps(): { [DEN_PROSE_ATTR]: "" } {
  return { [DEN_PROSE_ATTR]: "" };
}
