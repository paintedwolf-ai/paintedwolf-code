import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Show, type JSX } from "solid-js";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";

type Props = {
  scope: "device" | "project";
  /** Same intro copy as the counterpart surface. */
  children: JSX.Element;
  /** Cross-link label, e.g. "Settings → Approvals". */
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
};

/** Settings lede: one intro line and an optional counterpart jump. */
export function SettingsScopeLede(props: Props) {
  const counterpartHint = () =>
    props.scope === "device"
      ? PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromSettings
      : PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromProject;

  return (
    <div
      class="den-settings-scope-lede"
      data-testid="settings-scope-lede"
      data-scope={props.scope}
    >
      <div class="den-settings-scope-lede__body">{props.children}</div>
      <Show when={props.counterpartLabel && props.onOpenCounterpart}>
        <div
          class="den-settings-scope-lede__jump"
          data-testid="settings-scope-counterpart-block"
        >
          <p
            class="den-settings-scope-lede__hint"
            data-testid="settings-scope-counterpart-hint"
          >
            {counterpartHint()}
          </p>
          <button
            type="button"
            class="den-settings-scope-lede__link"
            data-testid="settings-scope-counterpart"
            onClick={() => props.onOpenCounterpart?.()}
          >
            <span>{props.counterpartLabel}</span>
            <ThemeIcon slot="link" size={12} />
          </button>
        </div>
      </Show>
    </div>
  );
}
