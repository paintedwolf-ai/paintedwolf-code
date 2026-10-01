/** Prod-only: preventDefault every contextmenu the text-edit host did not claim. */

let guardAbort: AbortController | null = null;

function onContextMenu(event: MouseEvent): void {
  event.preventDefault();
}

/** Register after the text-edit listeners. Skipped in DEV. */
export function setupContextMenuGuard(): void {
  if (typeof document === "undefined") return;
  guardAbort?.abort();
  guardAbort = new AbortController();
  document.addEventListener("contextmenu", onContextMenu, {
    signal: guardAbort.signal,
  });
}

export function teardownContextMenuGuard(): void {
  guardAbort?.abort();
  guardAbort = null;
}
