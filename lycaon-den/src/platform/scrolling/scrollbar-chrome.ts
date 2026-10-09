import { OverlayScrollbars } from "overlayscrollbars";
import type { ScrollbarAxisModel, ScrollbarInput } from "./overlay-scrollbar-input.ts";
function instanceFor(host: HTMLElement) { return OverlayScrollbars(host) ?? undefined; }

const VIEWPORT_PERCENT_PROPERTY = "--os-viewport-percent";
const SCROLL_PERCENT_PROPERTY = "--os-scroll-percent";

/** Matches the scrollbar library's CSS geometry precision. */
function roundCssNumber(value: number): number {
  return Math.round(value * 1e4) / 1e4;
}

/** Visible fraction of the scroll range used to size the handle. */
function viewportPercent(clientSize: number, scrollSize: number): string {
  return String(roundCssNumber(scrollSize > 0 ? Math.min(1, clientSize / scrollSize) : 1));
}

/** Position of the handle along its track, as the library's scroll percent. */
function scrollPercent(offset: number, range: number): string {
  const fraction = range > 0 ? offset / range : 0;
  return String(roundCssNumber(Math.max(0, Math.min(1, fraction || 0))));
}

/** With a scroll timeline the library animates handle offsets and never publishes the percent. */
function handleOffsetsPublished(): boolean {
  return typeof ScrollTimeline === "undefined";
}

function setStyleValue(
  element: HTMLElement | undefined,
  property: string,
  value: string,
): void {
  if (element && element.style.getPropertyValue(property) !== value) {
    element.style.setProperty(property, value);
  }
}

export type ScrollbarGeometry = {
  verticalPercent: string;
  verticalPosition?: string;
  horizontalPercent: string;
  /** Handle offsets along each track; absent where a scroll timeline drives them. */
  handles?: { x: string; y: string };
};

class ScrollbarChrome {
  private readonly verticalModels = new WeakMap<HTMLElement, ScrollbarAxisModel>();
  private readonly inputBindings = new WeakMap<HTMLElement, ScrollbarInput>();
  setModel(host: HTMLElement, model: ScrollbarAxisModel): void { this.verticalModels.set(host, model); }
  bindInput(host: HTMLElement, input: ScrollbarInput): void { this.inputBindings.set(host, input); }
  forget(host: HTMLElement): void { this.verticalModels.delete(host); this.inputBindings.delete(host); }
  /** Geometry snapshot for the enclosing layout read phase. */
  read(host: HTMLElement): ScrollbarGeometry {
    const instance = instanceFor(host);
    const viewport = instance?.elements().scrollOffsetElement ?? host;
    const logical = this.verticalModels.get(host)?.read();
    // Non-overflowing logical hosts need no layout reads.
    const horizontalOverflow = !logical || instance?.state().hasOverflow?.x !== false;
    const handles = handleOffsetsPublished();
    const logicalRange = logical ? Math.max(1, logical.extent - logical.viewport) : 0;
    const verticalPosition = logical ? String(Math.max(0, Math.min(1, logical.offset / logicalRange))) : undefined;
    const clientHeight = logical ? 0 : viewport.clientHeight;
    const scrollHeight = logical ? 0 : viewport.scrollHeight;
    const clientWidth = horizontalOverflow ? viewport.clientWidth : 0;
    const scrollWidth = horizontalOverflow ? viewport.scrollWidth : 0;
    const geometry: ScrollbarGeometry = {
      verticalPercent: logical ? viewportPercent(logical.viewport, logical.extent)
        : viewportPercent(clientHeight, scrollHeight),
      verticalPosition,
      horizontalPercent: horizontalOverflow ? viewportPercent(clientWidth, scrollWidth) : "1",
    };
    if (handles) {
      geometry.handles = {
        x: horizontalOverflow ? scrollPercent(viewport.scrollLeft, scrollWidth - clientWidth) : "0",
        y: logical ? scrollPercent(logical.offset, logicalRange) : scrollPercent(viewport.scrollTop, scrollHeight - clientHeight),
      };
    }
    return geometry;
  }

  /** Keeps scrollbar geometry current during direct input. */
  place(
    host: HTMLElement,
    geometry?: ScrollbarGeometry,
    options: { reapplyInput?: boolean } = {},
  ): void {
    const elements = instanceFor(host)?.elements();
    const vertical = elements?.scrollbarVertical?.scrollbar;
    const horizontal = elements?.scrollbarHorizontal?.scrollbar;
    if (!vertical && !horizontal) return;
    const moved = options.reapplyInput === false ? false : this.inputBindings.get(host)?.refreshGeometry();
    const measured = !moved && geometry ? geometry : this.read(host);
    setStyleValue(vertical, VIEWPORT_PERCENT_PROPERTY, measured.verticalPercent);
    if (measured.verticalPosition !== undefined) {
      setStyleValue(vertical, "--den-scrollbar-viewport", measured.verticalPercent);
      setStyleValue(vertical, "--den-scrollbar-position", measured.verticalPosition);
    }
    setStyleValue(horizontal, VIEWPORT_PERCENT_PROPERTY, measured.horizontalPercent);
    if (measured.handles) {
      setStyleValue(vertical, SCROLL_PERCENT_PROPERTY, measured.handles.y);
      setStyleValue(horizontal, SCROLL_PERCENT_PROPERTY, measured.handles.x);
    }
  }

}
export const scrollbarChrome = new ScrollbarChrome();
