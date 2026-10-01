/** jsdom elements carry no scrolling behavior; a mocked scroller moves its offset like a browser's. */

function clampOffset(element: HTMLElement, top: number): number {
  const max = Math.max(0, element.scrollHeight - element.clientHeight);
  return Math.max(0, Math.min(max, top));
}

/** Gives an element the absolute and relative scrolling a real scroller performs. */
export function mockScrollerMotion(element: HTMLElement): void {
  element.scrollTo = (options?: ScrollToOptions | number, y?: number) => {
    const top = typeof options === "number" ? y : options?.top;
    if (top === undefined) return;
    element.scrollTop = clampOffset(element, top);
  };
  element.scrollBy = (options?: ScrollToOptions | number, y?: number) => {
    const delta = (typeof options === "number" ? y : options?.top) ?? 0;
    element.scrollTop = clampOffset(element, element.scrollTop + delta);
  };
}
