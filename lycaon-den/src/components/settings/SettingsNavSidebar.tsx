import { For, Show } from "solid-js";
import {
  APP_SETTINGS,
  type SettingsSection,
} from "../../settings/settings-nav-model.ts";
import { SettingsSectionList } from "./SettingsSectionList.tsx";

type Props = {
  section: SettingsSection;
  onSectionChange?: (section: SettingsSection) => void;
};

function StaticSettingsNavSidebar(props: { section: SettingsSection }) {
  return (
    <div class="den-settings-sidebar">
      <nav class="den-shell-nav-sub" aria-label="Settings sections" data-testid="settings-nav-sections">
        <ul class="den-shell-nav-sub-list den-settings-sidebar-list">
          <For each={APP_SETTINGS}>
            {(item) => (
              <li>
                <div class="den-settings-sidebar-row">
                  <button
                    type="button"
                    class="den-shell-nav-sub-link"
                    classList={{ "den-shell-nav-sub-link-active": props.section === item.id }}
                    data-testid={`settings-nav-${item.id}`}
                  >
                    <Show when={props.section === item.id}>
                      <span class="den-nav-marker" aria-hidden="true" />
                    </Show>
                    <span class="den-shell-nav-sub-link-label">{item.label}</span>
                  </button>
                </div>
              </li>
            )}
          </For>
        </ul>
      </nav>
    </div>
  );
}

/** App-level settings section picker nested under the Settings nav link. */
export function SettingsNavSidebar(props: Props) {
  return (
    <Show
      when={props.onSectionChange}
      fallback={<StaticSettingsNavSidebar section={props.section} />}
    >
      <SettingsSectionList
        items={APP_SETTINGS}
        active={props.section}
        ariaLabel="Settings sections"
        testidPrefix="settings-nav"
        onSelect={(id) => props.onSectionChange?.(id as SettingsSection)}
      />
    </Show>
  );
}
