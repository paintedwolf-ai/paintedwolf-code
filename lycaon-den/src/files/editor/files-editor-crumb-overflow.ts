/** Clip fade for the editor breadcrumb from a trailing sentinel. */
export function observeCrumbClip(
  crumb: HTMLElement,
  sentinel: HTMLElement,
  onClip: (clipped: boolean) => void,
): () => void {
  if (typeof IntersectionObserver === "function") {
    const io = new IntersectionObserver(
      (entries) => {
        const entry = entries[entries.length - 1];
        if (entry) onClip(entry.intersectionRatio < 1);
      },
      { root: crumb, threshold: 1 },
    );
    io.observe(sentinel);
    return () => io.disconnect();
  }
  const sync = () => {
    onClip(crumb.scrollWidth > crumb.clientWidth + 1);
  };
  sync();
  if (typeof ResizeObserver === "undefined") return () => undefined;
  const ro = new ResizeObserver(sync);
  ro.observe(crumb);
  ro.observe(sentinel);
  return () => ro.disconnect();
}
