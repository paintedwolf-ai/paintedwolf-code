import { type Accessor, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { useTriggerGroup } from "./DenTriggerGroup.tsx";

type Props = {
  label: string;
  open: boolean;
  popup: "menu" | "dialog" | "listbox";
  onClick: () => void;
  onKeyDown?: JSX.EventHandlerUnion<HTMLButtonElement, KeyboardEvent>;
  onContextMenu?: JSX.EventHandlerUnion<HTMLButtonElement, MouseEvent>;
  /** Id of the open popup. */
  controls?: string;
  /** Marks a trigger whose popover holds non-default choices. */
  active?: boolean;
  disabled?: boolean;
  testId?: string;
  /** Fills the container for standalone triggers. */
  fill?: boolean;
  class?: string;
  buttonRef?: (element: HTMLButtonElement) => void;
};

/** Select-styled trigger for a custom popup. */
export function DenMenuTrigger(props: Props) {
  // Grouped triggers share one tab stop and render as adjacent segments.
  const segment = useTriggerGroup();

  const trigger = (className: Accessor<string>) => (
    <button
      ref={props.buttonRef}
      type="button"
      class={className()}
      data-testid={props.testId}
      data-open={segment && props.open ? "true" : undefined}
      aria-haspopup={props.popup}
      aria-expanded={props.open}
      aria-controls={props.open ? props.controls : undefined}
      aria-pressed={props.active}
      disabled={props.disabled}
      onClick={() => props.onClick()}
      onKeyDown={props.onKeyDown}
      onContextMenu={props.onContextMenu}
    >
      <span class="den-select__value">{props.label}</span>
      <span class="den-select__caret" aria-hidden="true" />
    </button>
  );

  if (segment) return trigger(() => cn("den-trigger-group__segment", props.class));

  return (
    <div
      class={cn("den-select", props.class)}
      data-fill={props.fill ? "true" : undefined}
      data-open={props.open ? "true" : undefined}
      data-disabled={props.disabled ? "true" : undefined}
    >
      {trigger(() => "den-select__trigger")}
    </div>
  );
}
