export type TranscriptRowsChanged = (rows: readonly HTMLElement[]) => void;

/** Identifies dirty rows for the geometry coordinator's measurement queue. */
export function observeTranscriptRowMutations(
  root: HTMLElement,
  onRowsChanged: TranscriptRowsChanged,
): () => void {
  if (typeof MutationObserver === "undefined") return () => {};
  const observer = new MutationObserver((mutations) => {
    const rows = new Set<HTMLElement>();
    for (const mutation of mutations) {
      const element = mutation.target instanceof Element ? mutation.target : mutation.target.parentElement;
      const row = element?.closest<HTMLElement>(".transcript-viewport-row");
      if (row?.isConnected && root.contains(row)) rows.add(row);
    }
    if (rows.size > 0) onRowsChanged([...rows]);
  });
  observer.observe(root, { subtree: true, childList: true, characterData: true,
    attributes: true, attributeFilter: ["hidden", "open"] });
  return () => observer.disconnect();
}
