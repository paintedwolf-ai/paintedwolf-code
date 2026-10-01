import { Show, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

export type SettingsListChromeProps = {
  count: JSX.Element | string;
  /** Primary trailing action (usually Add). Omit when the list has no add affordance. */
  action?: JSX.Element;
  secondaryAction?: JSX.Element;
  testId?: string;
  class?: string;
};

/** Count on the left; the primary action sits rightmost. */
export function SettingsListChrome(props: SettingsListChromeProps) {
  return (
    <div
      class={cn("den-settings-list-chrome", props.class)}
      data-testid={props.testId}
    >
      <div class="den-settings-list-chrome__count">{props.count}</div>
      <Show when={props.secondaryAction != null || props.action != null}>
        <div class="den-settings-list-chrome__actions">
          <Show when={props.secondaryAction}>{props.secondaryAction}</Show>
          <Show when={props.action}>{props.action}</Show>
        </div>
      </Show>
    </div>
  );
}
