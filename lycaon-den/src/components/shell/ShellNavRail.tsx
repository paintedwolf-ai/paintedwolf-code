import { onCleanup, onMount, type JSX } from "solid-js";
import { attachThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";

/** The project rail. Its scrollbar sits in the frame, outside the scroll extent it measures. */
export function ShellNavRail(props: { children: JSX.Element }) {
  let frame!: HTMLDivElement;
  let nav!: HTMLElement;
  let content!: HTMLDivElement;
  onMount(() => {
    onCleanup(attachThemedViewportScrollbar(frame, nav, { axis: "y", extent: content }));
  });
  return (
    <div ref={frame} class="den-shell-nav-frame">
      <nav ref={nav} class="den-shell-nav" aria-label="Main">
        <div ref={content} class="den-shell-nav-content">
          {props.children}
        </div>
      </nav>
    </div>
  );
}
