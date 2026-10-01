import { Show } from "solid-js";

export type BrowseChipProps = {
  label: string;
  active?: boolean;
  onClick?: () => void;
  onRemove?: () => void;
  disabled?: boolean;
  testId?: string;
  /** Action chips use compact toolbar spacing. */
  tone?: "filter" | "action";
};

export function BrowseChip(props: BrowseChipProps) {
  const tone = () => props.tone ?? "filter";
  return (
    <span
      class="den-browse-chip"
      classList={{
        "den-browse-chip--active": Boolean(props.active),
        "den-browse-chip--action": tone() === "action",
        "den-browse-chip--disabled": Boolean(props.disabled),
      }}
      data-testid={props.onClick ? undefined : props.testId}
    >
      <Show
        when={props.onClick}
        fallback={<span class="den-browse-chip__label">{props.label}</span>}
      >
        <button
          type="button"
          class="den-browse-chip__btn"
          data-testid={props.testId}
          disabled={props.disabled}
          aria-pressed={props.active}
          onClick={() => props.onClick?.()}
        >
          {props.label}
        </button>
      </Show>
      <Show when={props.onRemove}>
        <button
          type="button"
          class="den-browse-chip__remove"
          disabled={props.disabled}
          aria-label={`Remove ${props.label}`}
          onClick={(e) => {
            e.stopPropagation();
            props.onRemove?.();
          }}
        >
          ×
        </button>
      </Show>
    </span>
  );
}
