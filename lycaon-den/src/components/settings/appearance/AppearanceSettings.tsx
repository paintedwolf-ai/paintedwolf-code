/** Appearance mode and theme settings. */
import { For, Show, createMemo } from "solid-js";
import { presentedContributionThemes } from "../../../contributions/contribution-store.ts";
import type { ContributionTheme } from "../../../api/types.ts";
import {
  appearanceSelection,
  setAppearanceMode,
  setAppearanceTheme,
} from "../../../settings/appearance/appearance-prefs.ts";
import type { AppearanceMode } from "../../../contributions/theme-application.ts";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

const MODES: readonly { id: AppearanceMode; label: string; hint: string }[] = [
  { id: "system", label: "Match system", hint: "Follows your OS setting." },
  { id: "light", label: "Light", hint: "Always the light theme." },
  { id: "dark", label: "Dark", hint: "Always the dark theme." },
];

/** Themes available for one appearance. */
function themesFor(scheme: "light" | "dark"): ContributionTheme[] {
  return (presentedContributionThemes() ?? [])
    .filter((theme) => theme.appearance === scheme)
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** Keep a saved id visible when the pack that supplied it is gone. */
function themeSelectOptions(
  themes: readonly ContributionTheme[],
  selectedId: string,
): { value: string; label: string }[] {
  const options = themes.map((theme) => ({
    value: theme.id,
    label: theme.name,
  }));
  if (selectedId && !themes.some((theme) => theme.id === selectedId)) {
    options.unshift({
      value: selectedId,
      label: `${selectedId} — not installed`,
    });
  }
  return options;
}

export function AppearanceSettings() {
  const selection = createMemo(() => appearanceSelection());
  const light = createMemo(() => themesFor("light"));
  const dark = createMemo(() => themesFor("dark"));
  // Keep the stock palette visible until themes load.
  const hydrated = createMemo(() => light().length + dark().length > 0);
  const missingThemeIds = createMemo(() => {
    const sel = selection();
    const missing: string[] = [];
    if (sel.lightTheme && !light().some((theme) => theme.id === sel.lightTheme)) {
      missing.push(sel.lightTheme);
    }
    if (sel.darkTheme && !dark().some((theme) => theme.id === sel.darkTheme)) {
      missing.push(sel.darkTheme);
    }
    return missing;
  });

  return (
    <div class="den-settings-pref-group" data-testid="appearance-settings">
      <div class="den-settings-pref-row" {...settingAnchor("appearance")}>
        <div class="den-settings-pref-copy">
          <span class="den-settings-pref-label">{settingLabel("appearance")}</span>
          <p class="den-settings-hint">
            {MODES.find((m) => m.id === selection().mode)?.hint}
          </p>
        </div>
        <DenSelect
          class="den-settings-select"
          aria-label={settingLabel("appearance")}
          data-testid="appearance-mode"
          value={selection().mode}
          options={MODES.map((mode) => ({ value: mode.id, label: mode.label }))}
          onValueChange={(value) => {
            void setAppearanceMode(value as AppearanceMode);
          }}
        />
      </div>

      <Show
        when={hydrated()}
        fallback={
          <p
            class="den-settings-hint den-settings-pref-note"
            data-testid="appearance-unhydrated"
          >
            Connecting to the host. The stock palette is in use until the theme
            list arrives.
          </p>
        }
      >
        <For
          each={
            [
              ["light", "light-theme", light()],
              ["dark", "dark-theme", dark()],
            ] as const
          }
        >
          {([scheme, setting, themes]) => (
            <div class="den-settings-pref-row" {...settingAnchor(setting)}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel(setting)}</span>
                <p class="den-settings-hint">
                  Used when the window is {scheme}.
                </p>
              </div>
              <DenSelect
                class="den-settings-select"
                aria-label={settingLabel(setting)}
                data-testid={`appearance-theme-${scheme}`}
                value={
                  scheme === "dark"
                    ? selection().darkTheme
                    : selection().lightTheme
                }
                options={themeSelectOptions(
                  themes,
                  scheme === "dark"
                    ? selection().darkTheme
                    : selection().lightTheme,
                )}
                onValueChange={(value) => {
                  void setAppearanceTheme(value, scheme);
                }}
              />
            </div>
          )}
        </For>
        <Show when={missingThemeIds().length > 0}>
          <p
            class="den-settings-hint den-settings-pref-note"
            data-testid="appearance-fallback"
          >
            {missingThemeIds().join(", ")} isn't installed on this device. The
            stock theme is in use until you install it.
          </p>
        </Show>
      </Show>
    </div>
  );
}
