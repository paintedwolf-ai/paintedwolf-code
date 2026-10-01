import { Show, type JSX } from "solid-js";

type Props = {
  ref?: (element: HTMLElement) => void;
  onClick: JSX.EventHandler<HTMLElement, MouseEvent>;
  label?: string;
  accordion?: boolean;
  children: JSX.Element;
};

export function TranscriptChickletSummary(props: Props) {
  return (
    <summary
      ref={props.ref}
      class="den-transcript-chicklet-summary"
      onClick={props.onClick}
      aria-label={props.label}
    >
      <span class="den-tool-part-chicklet">
        <span class="den-tool-part-chicklet-main">{props.children}</span>
        <Show
          when={props.accordion}
          fallback={<span class="den-tool-chicklet-caret" aria-hidden="true" />}
        >
          <span class="den-row-mark" aria-hidden="true" />
        </Show>
      </span>
    </summary>
  );
}
