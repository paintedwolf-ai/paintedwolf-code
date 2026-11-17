import { For, Show } from "solid-js";
import type { ProjectSettingsRejectedRow } from "../../api/types.ts";
import { PROJECT_SETTINGS_REJECTED_COPY } from "../../settings/project-settings-rejected-copy.ts";

/** A project settings file the host could not read, what applies instead, and why. */
export function ProjectSettingsRejected(props: {
  rows: readonly ProjectSettingsRejectedRow[] | undefined;
  consequence: string;
  testId: string;
}) {
  return (
    <Show when={(props.rows?.length ?? 0) > 0}>
      <section class="den-settings-section" role="alert" data-testid={props.testId}>
        <h3 class="den-settings-overline">{PROJECT_SETTINGS_REJECTED_COPY.heading}</h3>
        <p class="den-settings-hint">{props.consequence}</p>
        <ul class="den-settings-rules">
          <For each={props.rows}>
            {(row) => (
              <li data-code={row.code}>
                <p class="den-settings-hint">{row.detail}</p>
              </li>
            )}
          </For>
        </ul>
      </section>
    </Show>
  );
}
