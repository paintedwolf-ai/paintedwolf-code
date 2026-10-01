/** Sets non-zero clientHeight on the scroller so the viewport measures visible rows under jsdom. */
export function mockReaderViewport(height = 600): () => void {
  const held = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.classList.contains("cm-scroller") ? height : 0;
    },
  });
  return () => {
    if (held) Object.defineProperty(HTMLElement.prototype, "clientHeight", held);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  };
}
