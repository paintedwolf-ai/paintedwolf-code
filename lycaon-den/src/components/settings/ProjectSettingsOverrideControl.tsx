import { Show } from "solid-js";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";

type Props = {
  /** Settings → path label, e.g. "Approvals". */
  settingsPath: string;
  /** True when this project has an explicit overlay. */
  enabled: boolean;
  disabled?: boolean;
  /** Short summary of Settings values while override is off. */
  followingSummary?: string;
  onChange: (enabled: boolean) => void;
};

/** Single top-of-panel control: follow Settings (default) vs project override. */
export function ProjectSettingsOverrideControl(props: Props) {
  return (
    <div
      class="den-project-override-card"
      data-testid="project-settings-override"
      data-enabled={props.enabled}
    >
      <div class="den-project-override-copy">
        <strong class="den-project-override-title">
          {PROJECT_SETTINGS_OVERLAY_COPY.overrideTitle(props.settingsPath)}
        </strong>
        <p class="den-settings-hint">{PROJECT_SETTINGS_OVERLAY_COPY.overrideHint}</p>
        <Show when={!props.enabled && props.followingSummary}>
          {(summary) => (
            <p
              class="den-project-override-summary"
              data-testid="project-override-summary"
            >
              {PROJECT_SETTINGS_OVERLAY_COPY.followingSettings(summary())}
            </p>
          )}
        </Show>
      </div>
      <DenCheckbox
        class="den-project-override-toggle"
        checked={props.enabled}
        disabled={props.disabled}
        data-testid="project-override-toggle"
        onChange={(e) => props.onChange(e.currentTarget.checked)}
      >
        {PROJECT_SETTINGS_OVERLAY_COPY.overrideToggleLabel}
      </DenCheckbox>
    </div>
  );
}
