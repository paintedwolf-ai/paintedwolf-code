/** Move DOM focus without allowing the browser to scroll an ancestor. */
export function focusWithoutScroll<T extends HTMLElement>(
  element: T | null | undefined,
): T | null {
  if (!element?.isConnected) return null;
  element.focus({ preventScroll: true });
  return document.activeElement === element ? element : null;
}

/** Focus the first element matching `selector` inside `root`. */
export function focusFirstWithoutScroll<T extends HTMLElement>(
  root: ParentNode | null | undefined,
  selector: string,
): T | null {
  return focusWithoutScroll(root?.querySelector<T>(selector));
}
