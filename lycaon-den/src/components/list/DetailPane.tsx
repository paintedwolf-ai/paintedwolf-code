import { Show, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";

type DetailPaneProps = {
  eyebrow?: JSX.Element;
  meta?: JSX.Element;
  onClose: () => void;
  children: JSX.Element;
  class?: string;
  testId?: string;
  closeLabel?: string;
  draggable?: boolean;
  onDragStart?: (event: DragEvent) => void;
  provenanceAttrs?: Record<string, string | undefined>;
};

export function DetailPane(props: DetailPaneProps) {
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    e.stopPropagation();
    props.onClose();
  };

  return (
    <section
      class={cn("den-detail-pane", props.class)}
      data-testid={props.testId}
      {...(props.provenanceAttrs ?? {})}
      draggable={props.draggable}
      onDragStart={props.onDragStart}
      onKeyDown={onKeyDown}
    >
      <header class="den-detail-pane__head">
        <Show when={props.eyebrow}>
          <span class="den-detail-pane__eyebrow">{props.eyebrow}</span>
        </Show>
        <Show when={props.meta}>
          <span class="den-detail-pane__meta">{props.meta}</span>
        </Show>
        <button
          type="button"
          class="den-detail-pane__close"
          aria-label={props.closeLabel ?? "Close details"}
          data-testid="detail-close"
          onClick={() => props.onClose()}
        >
          ×
        </button>
      </header>
      <Scrollport class="den-detail-pane__body" contentClass="den-detail-pane__body-content">
        {props.children}
      </Scrollport>
    </section>
  );
}
