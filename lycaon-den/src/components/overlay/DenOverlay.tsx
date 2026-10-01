import type { JSX } from "solid-js";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";

type Props = {
  class?: string;
  "data-testid"?: string;
  onClick?: JSX.EventHandlerUnion<HTMLDivElement, MouseEvent>;
  children: JSX.Element;
};

export function DenOverlay(props: Props) {
  let overlayEl: HTMLDivElement | undefined;

  createModalFocusTrap(
    () => true,
    () =>
      overlayEl?.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]') ??
      overlayEl,
  );

  return (
    <div
      ref={overlayEl}
      class={props.class ? `den-overlay ${props.class}` : "den-overlay"}
      data-testid={props["data-testid"]}
      onClick={props.onClick}
    >
      <ChromeDragSurface class="den-overlay__chrome-drag" />
      {props.children}
    </div>
  );
}
