const SELECTION_COPY = "den-copy-selection";

/** Offers a menu's captured selection to the surface that owns its copy action. */
export function handleSelectionCopy(target: EventTarget | null, text: string): boolean {
  if (!target || typeof Node === "undefined" || !(target instanceof Node)) return false;
  return !target.dispatchEvent(new CustomEvent(SELECTION_COPY, { bubbles: true, cancelable: true, detail: text }));
}

export function bindSelectionCopy(element: HTMLElement, copy: (text: string) => void): () => void {
  const listener = (event: Event) => {
    event.preventDefault();
    copy((event as CustomEvent<string>).detail);
  };
  element.addEventListener(SELECTION_COPY, listener);
  return () => element.removeEventListener(SELECTION_COPY, listener);
}
