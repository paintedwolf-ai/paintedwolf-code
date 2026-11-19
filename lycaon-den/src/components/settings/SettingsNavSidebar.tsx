import { For, Show } from "solid-js";
import {
  APP_SETTINGS,
  type SettingsSection,
} from "../../settings/settings-nav-model.ts";
import { NavSelectionDot, navRow } from "../nav/NavSelectionDot.tsx";

type Props = {
  section: SettingsSection;
  onSectionChange?: (section: SettingsSection) => void;
};

export function SettingsNavSidebar(props: Props) {
  return (
    <div class="den-settings-sidebar">
      <nav class="den-shell-nav-sub" aria-label="Settings sections" data-testid="settings-nav-sections">
        <ul class="den-shell-nav-sub-list den-settings-sidebar-list">
          <Show when={props.onSectionChange}>
            <NavSelectionDot index={APP_SETTINGS.findIndex((item) => item.id === props.section)} />
          </Show>
          <For each={APP_SETTINGS}>
            {(item, index) => (
              <li {...navRow(index())}>
                <button
                  type="button"
                  class="den-shell-nav-sub-link"
                  classList={{ "den-shell-nav-sub-link-active": props.section === item.id }}
                  data-testid={`settings-nav-${item.id}`}
                  onClick={() => props.onSectionChange?.(item.id)}
                >
                  <Show when={!props.onSectionChange && props.section === item.id}>
                    <span class="den-nav-marker" aria-hidden="true" />
                  </Show>
                  <span class="den-shell-nav-sub-link-label">{item.label}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </nav>
    </div>
  );
}
