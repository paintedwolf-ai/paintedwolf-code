/**
 * Every themed scroll surface is a frame around the element that scrolls:
 *
 *   .den-scrollport[data-den-scrollport=<axis>]   frame: layout box, carries the scrollbar chrome
 *     > .den-scrollport__viewport                 the scroll container
 *       > .den-scrollport__content                the scroll extent
 *
 * The frame carries the scrollbar chrome outside the viewport so the thumb
 * stays stable during off-thread scrolling.
 */
import { DEN_SCROLLPORT_CLASS, DEN_SCROLLPORT_VIEWPORT_CLASS, DEN_SCROLLPORT_CONTENT_CLASS, DEN_SCROLLPORT_AXIS_ATTR, DEN_SCROLLPORT_DEFER_ATTR } from "./themed-scrollbars.ts";

export type ScrollportAxis = "x" | "y" | "both";

export type ScrollportFrameParts = {
  axis: ScrollportAxis;
  viewport: HTMLElement;
  content: HTMLElement;
};

function isScrollportAxis(value: string | null): value is ScrollportAxis {
  return value === "x" || value === "y" || value === "both";
}

/** The frame's viewport and content, or undefined when the markup breaks the contract. */
export function scrollportFrameParts(frame: HTMLElement): ScrollportFrameParts | undefined {
  const axis = frame.getAttribute(DEN_SCROLLPORT_AXIS_ATTR);
  const viewport = frame.firstElementChild;
  const content = viewport?.firstElementChild;
  if (
    !isScrollportAxis(axis) ||
    !(viewport instanceof HTMLElement) || !viewport.classList.contains(DEN_SCROLLPORT_VIEWPORT_CLASS) ||
    !(content instanceof HTMLElement) || !content.classList.contains(DEN_SCROLLPORT_CONTENT_CLASS)
  ) {
    return undefined;
  }
  return { axis, viewport, content };
}

/** Wraps `content` in scrollport markup, for HTML built outside Solid such as rendered markdown. */
export function wrapInScrollportFrame(content: HTMLElement, options: {
  frameClass: string;
  axis: ScrollportAxis;
  defer?: boolean;
  viewportAttributes?: Record<string, string>;
}): HTMLDivElement {
  const doc = content.ownerDocument;
  const frame = doc.createElement("div");
  frame.className = `${DEN_SCROLLPORT_CLASS} ${options.frameClass}`;
  frame.setAttribute(DEN_SCROLLPORT_AXIS_ATTR, options.axis);
  if (options.defer) frame.setAttribute(DEN_SCROLLPORT_DEFER_ATTR, "");
  const viewport = doc.createElement("div");
  viewport.className = DEN_SCROLLPORT_VIEWPORT_CLASS;
  for (const [name, value] of Object.entries(options.viewportAttributes ?? {})) {
    viewport.setAttribute(name, value);
  }
  content.replaceWith(frame);
  content.classList.add(DEN_SCROLLPORT_CONTENT_CLASS);
  viewport.append(content);
  frame.append(viewport);
  return frame;
}
