import { Show, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

export type SettingsListRowProps = {
  selected?: boolean;
  variant?: "default" | "rejected" | "warning";
  leading?: JSX.Element;
  primary: JSX.Element | string;
  secondary?: JSX.Element | string;
  status?: JSX.Element;
  trailing?: JSX.Element;
  /** Omit for an informational row: it renders with no control to press. */
  onSelect?: () => void;
  testId?: string;
  "aria-label"?: string;
  class?: string;
};

/** Leading controls sit outside the select hit target so checkboxes stay valid. */
export function SettingsListRow(props: SettingsListRowProps) {
  const variant = () => props.variant ?? "default";
  const body = () => (
    <>
      <span class="den-settings-list-row__body">
        <span class="den-settings-list-row__primary">{props.primary}</span>
        <Show when={props.secondary != null && props.secondary !== ""}>
          <span class="den-settings-list-row__secondary">{props.secondary}</span>
        </Show>
      </span>
      <Show when={props.status}>
        <span class="den-settings-list-row__status">{props.status}</span>
      </Show>
    </>
  );
  return (
    <div
      class={cn("den-settings-list-row", props.class)}
      classList={{
        "den-settings-list-row--selected": !!props.selected,
        "den-settings-list-row--rejected": variant() === "rejected",
        "den-settings-list-row--warning": variant() === "warning",
      }}
    >
      <Show when={props.leading}>
        <span class="den-settings-list-row__leading">{props.leading}</span>
      </Show>
      <Show
        when={props.onSelect}
        fallback={
          <div
            class="den-settings-list-row__hit den-settings-list-row__hit--static"
            data-testid={props.testId}
            aria-label={props["aria-label"]}
          >
            {body()}
          </div>
        }
      >
        {(select) => (
          <button
            type="button"
            class="den-settings-list-row__hit"
            data-testid={props.testId}
            aria-label={props["aria-label"]}
            aria-pressed={!!props.selected}
            onClick={() => select()()}
          >
            {body()}
          </button>
        )}
      </Show>
      <Show when={props.trailing}>
        <span class="den-settings-list-row__trailing">{props.trailing}</span>
      </Show>
    </div>
  );
}
