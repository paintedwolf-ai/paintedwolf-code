import { onMount, splitProps, type JSX } from "solid-js";
import { Dynamic } from "solid-js/web";
import { syncThemedScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { type ScrollportAxis } from "../../platform/scrolling/scrollport-frame-dom.ts";
import { DEN_SCROLLPORT_AXIS_ATTR, DEN_SCROLLPORT_CLASS, DEN_SCROLLPORT_CONTENT_CLASS, DEN_SCROLLPORT_DEFER_ATTR, DEN_SCROLLPORT_VIEWPORT_CLASS } from "../../platform/scrolling/themed-scrollbars.ts";

type ContentTag = "div" | "ul" | "ol" | "dl" | "section" | "nav" | "footer" | "pre" | "form";

type DataAttributes = { [name: `data-${string}`]: string | undefined };

export type ScrollportProps = Omit<JSX.HTMLAttributes<HTMLDivElement>, "class" | "classList" | "ref" | "children" | "content"> & {
  /** Frame classes: the box's size, placement and paint. */
  class?: string;
  classList?: Record<string, boolean | undefined>;
  /** Content classes: padding and the layout of the children. */
  contentClass?: string;
  contentClassList?: Record<string, boolean | undefined>;
  /** The element holding the children, for list and landmark semantics. */
  contentAs?: ContentTag;
  /** Axes the viewport scrolls. */
  axis?: ScrollportAxis;
  /** Attaches in the mount frame, for surfaces that open already overflowing. */
  eager?: boolean;
  /** Attaches only once the frame approaches view, for frames repeated through long documents. */
  defer?: boolean;
  ref?: (frame: HTMLDivElement) => void;
  viewportRef?: (viewport: HTMLDivElement) => void;
  contentRef?: (content: HTMLElement) => void;
  /** Attributes of the element that scrolls: keyboard focus, region labels, scroll handlers. */
  viewport?: JSX.HTMLAttributes<HTMLDivElement> & DataAttributes;
  /** Attributes of the content element. */
  content?: JSX.HTMLAttributes<HTMLElement> & DataAttributes;
  children?: JSX.Element;
};

/**
 * A themed scroll surface. The frame carries the scrollbar outside the element that scrolls,
 * so the thumb never rides the scrolling thread ahead of the main thread.
 */
export function Scrollport(props: ScrollportProps) {
  const [local, frameProps] = splitProps(props, [
    "class", "classList", "contentClass", "contentClassList", "contentAs", "axis", "eager", "defer",
    "ref", "viewportRef", "contentRef", "viewport", "content", "children",
  ]);
  let frame!: HTMLDivElement;
  onMount(() => {
    if (local.eager) syncThemedScrollbar(frame);
  });
  return (
    <div
      {...frameProps}
      ref={(el) => {
        frame = el;
        local.ref?.(el);
      }}
      class={local.class ? `${DEN_SCROLLPORT_CLASS} ${local.class}` : DEN_SCROLLPORT_CLASS}
      classList={local.classList}
      {...{
        [DEN_SCROLLPORT_AXIS_ATTR]: local.axis ?? "y",
        [DEN_SCROLLPORT_DEFER_ATTR]: local.defer ? "" : undefined,
      }}
    >
      <div
        {...local.viewport}
        ref={(el) => local.viewportRef?.(el)}
        class={DEN_SCROLLPORT_VIEWPORT_CLASS}
      >
        <Dynamic
          component={local.contentAs ?? "div"}
          {...local.content}
          ref={(el: HTMLElement) => local.contentRef?.(el)}
          class={local.contentClass ? `${DEN_SCROLLPORT_CONTENT_CLASS} ${local.contentClass}` : DEN_SCROLLPORT_CONTENT_CLASS}
          classList={local.contentClassList}
        >
          {local.children}
        </Dynamic>
      </div>
    </div>
  );
}
