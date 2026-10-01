type PaletteRead = (property: string) => string;
const contrastQueries = new WeakMap<HTMLElement, MediaQueryList>();

function contrastQuery(root: HTMLElement): MediaQueryList | undefined {
  const current = contrastQueries.get(root);
  if (current) return current;
  const query = root.ownerDocument.defaultView?.matchMedia?.("(prefers-contrast: more)");
  if (query) contrastQueries.set(root, query);
  return query;
}

/** Each document resolves a recipe once for its current theme and contrast. */
export function createPaletteReader<T>(
  build: (read: PaletteRead, increasedContrast: boolean) => T,
): (root: HTMLElement) => T {
  const cache = new WeakMap<HTMLElement, { key: string; value: T }>();
  return (root) => {
    const contrast = contrastQuery(root)?.matches ?? false;
    const key = `${themeVersion(root)}:${contrast}`;
    const prior = cache.get(root);
    if (prior?.key === key) return prior.value;
    const styles = root.ownerDocument.defaultView?.getComputedStyle(root);
    const value = build((property) => styles?.getPropertyValue(property).trim() ?? "", contrast);
    cache.set(root, { key, value });
    return value;
  };
}

type ThemeVersion = { raw: string; signature: string; version: number };
const versions = new WeakMap<HTMLElement, ThemeVersion>();

function themeVersion(root: HTMLElement): number {
  const raw = `${root.getAttribute("data-den-appearance")}\0${root.getAttribute("style")}`;
  const previous = versions.get(root);
  if (previous?.raw === raw) return previous.version;
  const signature = themeSignature(root);
  const version = (previous?.version ?? 0) + (previous?.signature === signature ? 0 : 1);
  versions.set(root, { raw, signature, version });
  return version;
}

type PaletteListener = { changed(): void; windowPaint: boolean };
type ThemeWatch = { listeners: Set<PaletteListener>; stop(): void };
const watches = new WeakMap<HTMLElement, ThemeWatch>();

function themeSignature(root: HTMLElement): string {
  // Exclude derived window colors to prevent recipe invalidation loops.
  const properties = Array.from(root.style).filter((property) => !property.startsWith("--den-current-window-"));
  return JSON.stringify([root.getAttribute("data-den-appearance"), ...properties.sort().map((property) =>
    [property, root.style.getPropertyValue(property), root.style.getPropertyPriority(property)])]);
}

function windowPaintSignature(root: HTMLElement): string {
  const properties = Array.from(root.style).filter((property) => property.startsWith("--den-current-window-"));
  return JSON.stringify(properties.sort().map((property) =>
    [property, root.style.getPropertyValue(property), root.style.getPropertyPriority(property)]));
}

/** Recipe consumers ignore the window colors derived from that recipe. */
export function watchPaletteTheme(root: HTMLElement, changed: () => void): () => void {
  return subscribePalette(root, changed, false);
}

/** Painted chrome also follows changes to the current window's derived colors. */
export function watchWindowIdentityPaint(root: HTMLElement, changed: () => void): () => void {
  return subscribePalette(root, changed, true);
}

/** One observer and media listener per root notify all palette consumers together. */
function subscribePalette(root: HTMLElement, changed: () => void, windowPaint: boolean): () => void {
  let watch = watches.get(root);
  if (!watch) {
    const listeners = new Set<PaletteListener>();
    let signature = themeSignature(root);
    let paint = windowPaintSignature(root);
    let frame: number | undefined;
    const win = root.ownerDocument.defaultView;
    const media = contrastQuery(root);
    let contrast = media?.matches ?? false;
    const deliver = () => {
      frame = undefined;
      const next = themeSignature(root);
      const nextContrast = media?.matches ?? false;
      const nextPaint = windowPaintSignature(root);
      const themeChanged = next !== signature || nextContrast !== contrast;
      const paintChanged = nextPaint !== paint;
      signature = next;
      contrast = nextContrast;
      paint = nextPaint;
      for (const listener of [...listeners]) {
        if (listeners.has(listener) && (themeChanged || (listener.windowPaint && paintChanged))) listener.changed();
      }
    };
    const schedule = () => {
      if (frame === undefined) frame = win?.requestAnimationFrame(deliver);
    };
    const observer = new MutationObserver(schedule);
    observer.observe(root, { attributes: true, attributeFilter: ["style", "data-den-appearance"] });
    media?.addEventListener("change", schedule);
    watch = { listeners, stop: () => {
      observer.disconnect();
      media?.removeEventListener("change", schedule);
      if (frame !== undefined) win?.cancelAnimationFrame(frame);
    } };
    watches.set(root, watch);
  }
  const current = watch;
  const listener: PaletteListener = { changed, windowPaint };
  current.listeners.add(listener);
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    current.listeners.delete(listener);
    if (current.listeners.size) return;
    current.stop();
    watches.delete(root);
  };
}
