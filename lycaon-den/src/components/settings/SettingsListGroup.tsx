import { Show, type JSX } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";

export type SettingsListGroupProps = {
  label: string;
  meta?: string;
  action?: JSX.Element;
  testId?: string;
  children: JSX.Element;
};

/** Header over a run of settings list rows. */
export function SettingsListGroup(props: SettingsListGroupProps) {
  return (
    <section
      class="den-settings-list-group"
      data-testid={props.testId}
    >
      <header class="den-settings-list-group__head" {...chromeProps()}>
        <div class="den-settings-list-group__title">
          {/* h3 sits under the panel's h2; the class carries the look. */}
          <h3 class="den-settings-subhead">{props.label}</h3>
          <Show when={props.meta}>
            <span class="den-settings-hint">{props.meta}</span>
          </Show>
        </div>
        <Show when={props.action}>
          <span class="den-settings-list-group__action">{props.action}</span>
        </Show>
      </header>
      {props.children}
    </section>
  );
}
